package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

// permissionsState records what zakwas wrote for agents.permissions, by
// absolute file path, so it removes only its own rules, also from a config
// dir the provider no longer uses.
type permissionsState struct {
	// Claude maps a settings.json to the rules zakwas added to it.
	Claude map[string]claudeRules `json:"claude,omitempty"`
	// Codex maps a rules file zakwas wrote to the SHA-256 of its content.
	Codex map[string]string `json:"codex,omitempty"`
}

// claudeRules are Claude Code permission rules, as in settings.json.
type claudeRules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

func (r claudeRules) empty() bool { return len(r.Allow) == 0 && len(r.Deny) == 0 }

func (r claudeRules) equal(o claudeRules) bool {
	return slices.Equal(r.Allow, o.Allow) && slices.Equal(r.Deny, o.Deny)
}

// union keeps every rule of both, for the record written before an edit:
// a rule zakwas added must never go untracked.
func (r claudeRules) union(o claudeRules) claudeRules {
	merge := func(a, b []string) []string {
		out := slices.Clone(a)
		for _, s := range b {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		return out
	}
	return claudeRules{Allow: merge(r.Allow, o.Allow), Deny: merge(r.Deny, o.Deny)}
}

// PermissionsStatePath is the ownership record for agents.permissions.
func PermissionsStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "agent-permissions.json")
}

func (m *Module) permissionsStatePath() string { return PermissionsStatePath(m.Paths.Home) }

func (m *Module) loadPermissionsState() (permissionsState, error) {
	var st permissionsState
	if err := loadState(m.permissionsStatePath(), &st); err != nil {
		return st, err
	}
	if st.Claude == nil {
		st.Claude = map[string]claudeRules{}
	}
	if st.Codex == nil {
		st.Codex = map[string]string{}
	}
	return st, nil
}

func (m *Module) updatePermissionsState(edit func(*permissionsState)) error {
	path := m.permissionsStatePath()
	if err := install.CheckParents(m.Paths, path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", m.Paths.Pretty(path))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return err
	}
	edit(&st)
	if len(st.Claude) == 0 && len(st.Codex) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveState(path, st)
}

func (m *Module) claudeSettingsPath() string {
	return filepath.Join(m.claudeBackend().dir, "settings.json")
}

func (m *Module) codexRulesPath() string {
	return filepath.Join(m.codexBackend().home, "rules", "zakwas.rules")
}

// desiredClaudeRules maps allow and deny to Bash rules. review has no
// rule: Claude's auto mode reviews every command it doesn't allow, while an
// ask rule would stop for a person even in auto mode.
func (m *Module) desiredClaudeRules() claudeRules {
	var r claudeRules
	for _, p := range m.Agents.PermissionsFor(config.ProviderClaude) {
		rule := "Bash(" + p.Command() + ":*)"
		switch p.Decision {
		case config.PermissionAllow:
			r.Allow = append(r.Allow, rule)
		case config.PermissionDeny:
			r.Deny = append(r.Deny, rule)
		}
	}
	return r
}

// planPermissions plans agents.permissions for Claude Code and Codex by
// provider; opencode gets them through the agentPermissions template
// function instead. A provider whose files can't be planned is returned in
// failed.
func (m *Module) planPermissions() (map[string][]engine.Change, map[string]error, error) {
	statePath := m.permissionsStatePath()
	if m.Agents.Permissions == nil {
		if _, err := os.Lstat(statePath); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("agents.permissions: %s is a symlink", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, nil, fmt.Errorf("agents.permissions: %w", err)
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return nil, nil, err
	}
	changes := map[string][]engine.Change{}
	failed := map[string]error{}
	add := func(provider string, c *engine.Change, err error) {
		switch {
		case err != nil:
			failed[provider] = errors.Join(failed[provider], fmt.Errorf("%s: permissions: %w", provider, err))
		case c != nil:
			c.Group = provider
			changes[provider] = append(changes[provider], *c)
		}
	}

	settings := m.claudeSettingsPath()
	for _, path := range stalePaths(slices.Collect(maps.Keys(st.Claude)), settings) {
		c, err := m.planClaudePermissions(path, claudeRules{})
		add(config.ProviderClaude, c, err)
	}
	if want := m.desiredClaudeRules(); !want.empty() || !ownedClaudeRules(st, settings).empty() {
		c, err := m.planClaudePermissions(settings, want)
		add(config.ProviderClaude, c, err)
	}

	rules := m.codexRulesPath()
	for _, path := range stalePaths(slices.Collect(maps.Keys(st.Codex)), rules) {
		c, err := m.planCodexRules(path, nil)
		add(config.ProviderCodex, c, err)
	}
	if want := m.Agents.PermissionsFor(config.ProviderCodex); len(want) > 0 || ownedCodexRules(st, rules) != "" {
		c, err := m.planCodexRules(rules, want)
		add(config.ProviderCodex, c, err)
	}
	return changes, failed, nil
}

// sameFile keys a file by its real path, ignoring case like macOS's file
// system, so a settings.json reached through a dotfiles symlink and the
// link's target count as one file. A missing file falls back to samePath.
func sameFile(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return strings.ToLower(real)
	}
	return samePath(path)
}

// stalePaths are the recorded files other than current, from an earlier
// config dir, sorted and each file once however its path was spelled.
func stalePaths(recorded []string, current string) []string {
	seen := map[string]bool{sameFile(current): true}
	var out []string
	for _, path := range slices.Sorted(slices.Values(recorded)) {
		if !seen[sameFile(path)] {
			seen[sameFile(path)] = true
			out = append(out, path)
		}
	}
	return out
}

// ownedClaudeRules are the rules zakwas added to a settings file, by
// whatever path it recorded for the same file.
func ownedClaudeRules(st permissionsState, path string) claudeRules {
	var owned claudeRules
	for key, r := range st.Claude {
		if sameFile(key) == sameFile(path) {
			owned = owned.union(r)
		}
	}
	return owned
}

// recordClaudeRules records r as zakwas's rules in the settings at path,
// replacing what was recorded for the same file under any path.
func recordClaudeRules(path string, r claudeRules) func(*permissionsState) {
	return func(st *permissionsState) {
		for key := range st.Claude {
			if sameFile(key) == sameFile(path) {
				delete(st.Claude, key)
			}
		}
		if !r.empty() {
			st.Claude[path] = r
		}
	}
}

// ownedCodexRules is the SHA-256 of the rules file zakwas wrote at path,
// or empty.
func ownedCodexRules(st permissionsState, path string) string {
	for key, sum := range st.Codex {
		if sameFile(key) == sameFile(path) {
			return sum
		}
	}
	return ""
}

// renderClaudePermissions edits the permissions lists of settings data
// read from path, nil for a missing file: removes the rules zakwas added
// that are no longer wanted and adds the missing wanted ones. A wanted rule
// that is already there and not zakwas's stays the user's. Other keys keep
// their order and values.
func renderClaudePermissions(path string, data []byte, want, owned claudeRules) (o object, next claudeRules, diff []string, err error) {
	if data != nil {
		if o, err = parseObject(path, data); err != nil {
			return o, next, nil, err
		}
	}
	var perms object
	if raw, ok := o.get("permissions"); ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return o, next, nil, fmt.Errorf("%s permissions: %w", path, err)
		}
	}
	removedAny := false
	for _, list := range []struct {
		key         string
		want, owned []string
		next        *[]string
	}{
		{"allow", want.Allow, owned.Allow, &next.Allow},
		{"deny", want.Deny, owned.Deny, &next.Deny},
	} {
		var have []string
		raw, present := perms.get(list.key)
		if present {
			if err := json.Unmarshal(raw, &have); err != nil {
				return o, next, nil, fmt.Errorf("%s permissions.%s: %w", path, list.key, err)
			}
		}
		kept := make([]string, 0, len(have))
		removed := map[string]bool{}
		for _, rule := range have {
			// One copy is zakwas's; a second one is the user's.
			if slices.Contains(list.owned, rule) && !slices.Contains(list.want, rule) && !removed[rule] {
				diff = append(diff, "-"+list.key+" "+rule)
				removed[rule] = true
				continue
			}
			kept = append(kept, rule)
		}
		added := false
		for _, rule := range list.want {
			if slices.Contains(kept, rule) {
				if slices.Contains(list.owned, rule) {
					*list.next = append(*list.next, rule)
				}
				continue
			}
			kept = append(kept, rule)
			*list.next = append(*list.next, rule)
			diff = append(diff, "+"+list.key+" "+rule)
			added = true
		}
		switch {
		case len(removed) > 0 && len(kept) == 0:
			perms.delete(list.key)
			removedAny = true
		case len(removed) > 0 || added:
			if err := perms.set(list.key, kept); err != nil {
				return o, next, nil, err
			}
		}
	}
	if len(diff) == 0 {
		return o, next, nil, nil
	}
	if removedAny && len(perms.keys) == 0 {
		o.delete("permissions")
	} else if err := o.set("permissions", perms); err != nil {
		return o, next, nil, err
	}
	return o, next, diff, nil
}

// planClaudePermissions converges the rules zakwas owns in one Claude
// settings file to want; an empty want removes them.
func (m *Module) planClaudePermissions(path string, want claudeRules) (*engine.Change, error) {
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", m.Paths.Pretty(path))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return nil, err
	}
	owned := ownedClaudeRules(st, path)
	data, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	exists := data != nil
	_, next, diff, err := renderClaudePermissions(path, data, want, owned)
	if err != nil {
		return nil, err
	}
	if len(diff) == 0 {
		_, keyed := st.Claude[path]
		if next.equal(owned) && (keyed || next.empty()) {
			return nil, nil
		}
		detail := "track permission rules"
		if next.empty() {
			detail = "forget permission rules removed by hand"
		}
		return &engine.Change{
			Action: engine.Update, Target: m.Paths.Pretty(path), Detail: detail,
			Apply: func(context.Context) error { return m.updatePermissionsState(recordClaudeRules(path, next)) },
		}, nil
	}
	if exists {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o200 == 0 {
			return nil, fmt.Errorf("%s is read-only; change it in the repo and apply it first", m.Paths.Pretty(path))
		}
	}
	action := engine.Update
	if !exists {
		action = engine.Create
	}
	return &engine.Change{
		Action: action, Target: m.Paths.Pretty(path), Detail: permissionsDetail(diff),
		Diff: strings.Join(diff, "\n") + "\n",
		Apply: func(context.Context) error {
			for range claudeWriteAttempts {
				if done, err := m.applyClaudePermissions(path, want); done || err != nil {
					return err
				}
			}
			return fmt.Errorf("%s kept changing while zakwas edited it; run apply again", m.Paths.Pretty(path))
		},
	}, nil
}

// claudeWriteAttempts bounds the redos when Claude Code keeps writing its
// settings while zakwas edits them.
const claudeWriteAttempts = 3

// applyClaudePermissions edits the settings at path once. done is false
// when the file changed between the read and the write: nothing was
// written and the edit must be redone from the new content.
func (m *Module) applyClaudePermissions(path string, want claudeRules) (done bool, err error) {
	if err := install.CheckParents(m.Paths, path); err != nil {
		return false, err
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o200 == 0 {
			return false, fmt.Errorf("%s is not a writable regular file", m.Paths.Pretty(path))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return false, err
	}
	owned := ownedClaudeRules(st, path)
	data, err := readIfExists(path)
	if err != nil {
		return false, err
	}
	o, next, diff, err := renderClaudePermissions(path, data, want, owned)
	if err != nil {
		return false, err
	}
	if len(diff) == 0 {
		return true, m.updatePermissionsState(recordClaudeRules(path, next))
	}
	if err := m.updatePermissionsState(recordClaudeRules(path, owned.union(next))); err != nil {
		return false, err
	}
	if data != nil {
		err = writeObjectIf(path, o, data)
	} else {
		err = createObject(path, o, 0o600)
	}
	if errors.Is(err, errChanged) {
		return false, m.updatePermissionsState(recordClaudeRules(path, owned))
	}
	if err != nil {
		return false, err
	}
	return true, m.updatePermissionsState(recordClaudeRules(path, next))
}

// codexDecisions maps a decision to Codex's prefix_rule decision.
var codexDecisions = map[string]string{
	config.PermissionAllow:  "allow",
	config.PermissionReview: "prompt",
	config.PermissionDeny:   "forbidden",
}

// starlarkString quotes s for a rules file. Codex drops every rules file
// when one fails to parse, forbidden rules included, so anything that
// isn't a plain printable string is refused rather than escaped.
func starlarkString(s string) (string, error) {
	if strings.ContainsFunc(s, unicode.IsControl) || !utf8.ValidString(s) {
		return "", fmt.Errorf("%q: not a printable string", s)
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`, nil
}

// renderCodexRules writes one prefix_rule per entry. Codex applies the
// strictest decision among matching rules, so order doesn't matter.
func renderCodexRules(perms []config.Permission) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# Generated by zakwas from agents.permissions in zakwas.yaml; change it there.\n")
	for _, p := range perms {
		words := make([]string, len(p.Words))
		for i, w := range p.Words {
			q, err := starlarkString(w)
			if err != nil {
				return nil, err
			}
			words[i] = q
		}
		fmt.Fprintf(&b, "prefix_rule(pattern=[%s], decision=\"%s\"", strings.Join(words, ", "), codexDecisions[p.Decision])
		if p.Justification != "" {
			if strings.TrimSpace(p.Justification) == "" {
				return nil, fmt.Errorf("%q: blank justification, which Codex rejects", p.Command())
			}
			q, err := starlarkString(p.Justification)
			if err != nil {
				return nil, err
			}
			b.WriteString(", justification=" + q)
		}
		b.WriteString(")\n")
	}
	return b.Bytes(), nil
}

var codexDecision = regexp.MustCompile(`decision\s*=\s*"([a-z]+)"`)

// codexRulesDiff lists the rule lines only one of the files has.
func codexRulesDiff(have, want []byte) []string {
	lines := func(data []byte) []string {
		var out []string
		for l := range strings.Lines(string(data)) {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	old, next := lines(have), lines(want)
	var diff []string
	for _, l := range old {
		if !slices.Contains(next, l) {
			diff = append(diff, "-"+codexRuleDecision(l))
		}
	}
	for _, l := range next {
		if !slices.Contains(old, l) {
			diff = append(diff, "+"+codexRuleDecision(l))
		}
	}
	return diff
}

func codexRuleDecision(line string) string {
	if m := codexDecision.FindStringSubmatch(line); m != nil {
		return m[1] + " " + line
	}
	return "rule " + line
}

// permissionsDetail counts the added and removed rules by list, e.g.
// "+2 allow, -1 deny"; diff lines are "+<list> <rule>" or "-<list> <rule>".
func permissionsDetail(diff []string) string {
	var keys []string
	counts := map[string]int{}
	for _, d := range diff {
		key, _, _ := strings.Cut(d, " ")
		if counts[key] == 0 {
			keys = append(keys, key)
		}
		counts[key]++
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%c%d %s", k[0], counts[k], k[1:])
	}
	return "permissions " + strings.Join(parts, ", ")
}

// planCodexRules writes the Codex rules file zakwas owns, or removes it
// when no entry targets Codex. A file zakwas didn't write, or one edited
// since, is backed up before it is replaced and left in place on removal.
func (m *Module) planCodexRules(path string, perms []config.Permission) (*engine.Change, error) {
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	have, err := readRegular(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.Paths.Pretty(path), err)
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return nil, err
	}
	ownedSum := ownedCodexRules(st, path)
	forget := func(st *permissionsState) {
		for key := range st.Codex {
			if sameFile(key) == sameFile(path) {
				delete(st.Codex, key)
			}
		}
	}
	ours := func(data []byte, sum string) bool { return data != nil && sum != "" && install.Sum(data) == sum }
	current := func() ([]byte, string, error) {
		if err := install.CheckParents(m.Paths, path); err != nil {
			return nil, "", err
		}
		data, err := readRegular(path)
		if err != nil {
			return nil, "", err
		}
		st, err := m.loadPermissionsState()
		return data, ownedCodexRules(st, path), err
	}

	if len(perms) == 0 {
		if ownedSum == "" {
			return nil, nil
		}
		if !ours(have, ownedSum) {
			return &engine.Change{
				Action: engine.Remove, Target: m.Paths.Pretty(path), Detail: "no longer zakwas's rules, forget them",
				Apply: func(context.Context) error { return m.updatePermissionsState(forget) },
			}, nil
		}
		return &engine.Change{
			Action: engine.Remove, Target: m.Paths.Pretty(path), Detail: permissionsDetail(codexRulesDiff(have, nil)),
			Diff: udiff.Unified(path, "/dev/null", string(have), ""), Destructive: true,
			Apply: func(context.Context) error {
				data, sum, err := current()
				if err != nil {
					return err
				}
				if ours(data, sum) {
					if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return err
					}
				}
				return m.updatePermissionsState(forget)
			},
		}, nil
	}

	content, err := renderCodexRules(perms)
	if err != nil {
		return nil, err
	}
	sum := install.Sum(content)
	record := func(st *permissionsState) {
		forget(st)
		st.Codex[path] = sum
	}
	if bytes.Equal(have, content) {
		if _, keyed := st.Codex[path]; keyed && ownedSum == sum {
			return nil, nil
		}
		return &engine.Change{
			Action: engine.Update, Target: m.Paths.Pretty(path), Detail: "track permission rules",
			Apply: func(context.Context) error { return m.updatePermissionsState(record) },
		}, nil
	}
	action, detail := engine.Update, permissionsDetail(codexRulesDiff(have, content))
	switch {
	case have == nil:
		action = engine.Create
	case !ours(have, ownedSum):
		detail += ", back up to " + filepath.Base(install.BackupName(path))
	}
	return &engine.Change{
		Action: action, Target: m.Paths.Pretty(path), Detail: detail,
		Diff: udiff.Unified(path, "zakwas.yaml", string(have), string(content)),
		Apply: func(context.Context) error {
			data, owned, err := current()
			if err != nil {
				return err
			}
			if data != nil && !ours(data, owned) && !bytes.Equal(data, content) {
				if _, err := install.Backup(path); err != nil {
					return err
				}
			}
			if err := install.WriteAtomic(path, content, 0o644); err != nil {
				return err
			}
			return m.updatePermissionsState(record)
		},
	}, nil
}

// readRegular reads a regular file; a missing one is nil, anything else at
// the path (a symlink, a directory) an error.
func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, errors.New("not a regular file")
	}
	return os.ReadFile(path)
}
