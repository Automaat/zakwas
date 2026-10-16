package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

// mcpState records the MCP servers zakwas registered, by provider and
// absolute config file, with the SHA-256 of the canonical entry it wrote.
// Only those servers are changed or removed, also in a config dir the
// provider no longer uses.
type mcpState struct {
	Claude map[string]map[string]string `json:"claude,omitempty"`
	Codex  map[string]map[string]string `json:"codex,omitempty"`
	// Copilot servers live in mcp-config.json.
	Copilot map[string]map[string]string `json:"copilot,omitempty"`
}

func (st *mcpState) files(provider string) map[string]map[string]string {
	switch provider {
	case config.ProviderCodex:
		return st.Codex
	case config.ProviderCopilot:
		return st.Copilot
	}
	return st.Claude
}

func (st *mcpState) empty() bool {
	return len(st.Claude) == 0 && len(st.Codex) == 0 && len(st.Copilot) == 0
}

// owned merges what is recorded for one file under any spelling of its
// path.
func (st *mcpState) owned(provider, path string) map[string]string {
	out := map[string]string{}
	for key, servers := range st.files(provider) {
		if sameFile(key) == sameFile(path) {
			maps.Copy(out, servers)
		}
	}
	return out
}

// record sets the hash of one server in the file at path, or forgets the
// server when hash is empty.
func (st *mcpState) record(provider, path, name, hash string) {
	files := st.files(provider)
	servers := st.owned(provider, path)
	for key := range files {
		if sameFile(key) == sameFile(path) {
			delete(files, key)
		}
	}
	if hash == "" {
		delete(servers, name)
	} else {
		servers[name] = hash
	}
	if len(servers) > 0 {
		files[path] = servers
	}
}

// MCPStatePath is the ownership record for agents.mcp.
func MCPStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "agent-mcp.json")
}

func (m *Module) mcpStatePath() string { return MCPStatePath(m.Paths.Home) }

func (m *Module) loadMCPState() (mcpState, error) {
	st := mcpState{Claude: map[string]map[string]string{}, Codex: map[string]map[string]string{}, Copilot: map[string]map[string]string{}}
	if err := loadState(m.mcpStatePath(), &st); err != nil {
		return st, err
	}
	if st.Claude == nil {
		st.Claude = map[string]map[string]string{}
	}
	if st.Codex == nil {
		st.Codex = map[string]map[string]string{}
	}
	if st.Copilot == nil {
		st.Copilot = map[string]map[string]string{}
	}
	return st, nil
}

func (m *Module) updateMCPState(edit func(*mcpState)) error {
	path := m.mcpStatePath()
	if err := install.CheckParents(m.Paths, path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", m.Paths.Pretty(path))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	st, err := m.loadMCPState()
	if err != nil {
		return err
	}
	edit(&st)
	if st.empty() {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveState(path, st)
}

// mcpEntry is one server as a provider's config holds it. canon is the
// normalized entry as compact JSON, which makes entries comparable whatever
// their spelling; text is how it reads in the file.
type mcpEntry struct {
	canon  string
	text   string
	value  map[string]any
	inline bool
}

type mcpFile map[string]mcpEntry

// mcpStore reads and edits the servers one provider keeps in its config
// file.
type mcpStore interface {
	provider() string
	path() string
	load(path string) (mcpFile, error)
	render(name string, s config.MCPServer) (mcpEntry, error)
	set(path, name string, s config.MCPServer) error
	unset(path, name string) error
	loginHint(name string) string
	removeHint(name string) string
}

// normalizeMCP drops empty values, so a missing list and an empty one
// compare equal, and returns the entry's canonical JSON.
func normalizeMCP(v map[string]any) (map[string]any, string, error) {
	pruned := pruneMCP(v).(map[string]any)
	if pruned == nil {
		pruned = map[string]any{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(pruned); err != nil {
		return nil, "", err
	}
	return pruned, strings.TrimRight(b.String(), "\n"), nil
}

func pruneMCP(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			e = pruneMCP(e)
			switch c := e.(type) {
			case nil:
				continue
			case map[string]any:
				if len(c) == 0 {
					continue
				}
			case []any:
				if len(c) == 0 {
					continue
				}
			}
			out[k] = e
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = pruneMCP(e)
		}
		return out
	}
	return v
}

func prettyMCP(v map[string]any) string {
	data, err := marshal(v)
	if err != nil {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, data, "", "  "); err != nil {
		return string(data)
	}
	return out.String() + "\n"
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

func differingKeys(a, b map[string]any) []string {
	var keys []string
	for k := range a {
		if _, ok := b[k]; !ok || !mcpEqual(a[k], b[k]) {
			keys = append(keys, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func mcpEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

var urlInText = regexp.MustCompile(`https?://[^\s"'\\]+`)

// redactURLs hides the user info and query values of the URLs in a diff,
// which are where servers put keys; the summary line hides them too.
func redactURLs(text string) string {
	return urlInText.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		u.User = nil
		if u.RawQuery != "" {
			query := u.Query()
			for key := range query {
				query.Set(key, "REDACTED")
			}
			u.RawQuery = query.Encode()
		}
		return u.String()
	})
}

func mcpSummary(s config.MCPServer) string {
	if !s.IsHTTP() {
		return "stdio " + s.Command
	}
	u, err := url.Parse(s.URL)
	if err != nil {
		return "http"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return "http " + u.String()
}

// planMCP plans agents.mcp for Claude Code, Codex and Copilot by provider; opencode
// gets the servers through the agentMCP template function instead. A
// provider whose servers can't be planned is returned in failed.
func (m *Module) planMCP() (map[string][]engine.Change, map[string]error, error) {
	statePath := m.mcpStatePath()
	if len(m.Agents.MCP) == 0 {
		if _, err := os.Lstat(statePath); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("agents.mcp: %s is a symlink", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, nil, fmt.Errorf("agents.mcp: %w", err)
	}
	st, err := m.loadMCPState()
	if err != nil {
		return nil, nil, err
	}
	changes := map[string][]engine.Change{}
	failed := map[string]error{}
	for _, store := range []mcpStore{m.claudeMCP(), m.codexMCP(), m.copilotMCP()} {
		provider := store.provider()
		want := map[string]config.MCPServer{}
		for _, s := range m.Agents.MCPFor(provider) {
			want[s.Name] = s.MCPServer
		}
		var recorded []string
		for key := range st.files(provider) {
			recorded = append(recorded, key)
		}
		paths := append([]string{store.path()}, stalePaths(recorded, store.path())...)
		for i, path := range paths {
			servers := want
			if i > 0 {
				servers = nil
			}
			c, err := m.planMCPFile(store, path, servers, st)
			if err != nil {
				failed[provider] = errors.Join(failed[provider], fmt.Errorf("%s: mcp: %w", provider, err))
				continue
			}
			for j := range c {
				c[j].Group = provider
			}
			changes[provider] = append(changes[provider], c...)
		}
	}
	return changes, failed, nil
}

func (m *Module) planMCPFile(store mcpStore, path string, want map[string]config.MCPServer, st mcpState) ([]engine.Change, error) {
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	provider := store.provider()
	owned := st.owned(provider, path)
	if len(want) == 0 && len(owned) == 0 {
		return nil, nil
	}
	have, err := store.load(path)
	if err != nil {
		return nil, err
	}
	pretty := m.Paths.Pretty(path)
	names := slices.Sorted(maps.Keys(want))
	for _, name := range sortedKeys(owned) {
		if _, declared := want[name]; !declared {
			names = append(names, name)
		}
	}

	var changes []engine.Change
	var errs []error
	writes := false
	target := provider + " mcp "
	for _, name := range names {
		cur, present := have[name]
		hash, isOwned := owned[name]
		server, wanted := want[name]
		forget := func(detail string) engine.Change {
			return engine.Change{
				Action: engine.Update, Target: target + name, Detail: detail,
				Apply: func(context.Context) error {
					return m.updateMCPState(func(st *mcpState) { st.record(provider, path, name, "") })
				},
			}
		}
		if !wanted {
			switch {
			case !present:
				changes = append(changes, forget("already removed, stop tracking it"))
			case install.Sum([]byte(cur.canon)) != hash:
				changes = append(changes, forget("changed since zakwas added it, leave it and stop tracking it"))
			default:
				if cur.inline {
					errs = append(errs, fmt.Errorf("%q is defined inline in %s, which zakwas can't remove; delete it by hand", name, pretty))
					continue
				}
				writes = true
				changes = append(changes, m.removeMCPChange(store, path, name, hash, cur))
			}
			continue
		}
		next, err := store.render(name, server)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", name, err))
			continue
		}
		sum := install.Sum([]byte(next.canon))
		switch {
		case present && cur.canon == next.canon:
			if isOwned && hash == sum {
				continue
			}
			adopt := forget("adopt the server already there")
			adopt.Apply = func(context.Context) error {
				return m.updateMCPState(func(st *mcpState) { st.record(provider, path, name, sum) })
			}
			changes = append(changes, adopt)
		case present && !isOwned:
			errs = append(errs, fmt.Errorf("server %q is already in %s and differs from zakwas.yaml in %s; remove it with `%s` or make zakwas.yaml match, then apply again",
				name, pretty, strings.Join(differingKeys(cur.value, next.value), ", "), store.removeHint(name)))
		case present && install.Sum([]byte(cur.canon)) != hash:
			errs = append(errs, fmt.Errorf("server %q in %s was changed since zakwas added it and differs from zakwas.yaml in %s; restore it, or remove it with `%s` and apply again",
				name, pretty, strings.Join(differingKeys(cur.value, next.value), ", "), store.removeHint(name)))
		case present && cur.inline:
			errs = append(errs, fmt.Errorf("%q is defined inline in %s, which zakwas can't edit; change it by hand", name, pretty))
		default:
			writes = true
			action, detail := engine.Create, mcpSummary(server)
			from := ""
			if present {
				action, from = engine.Update, cur.text
			}
			if server.OAuth.On() {
				detail += ", then " + store.loginHint(name)
			}
			changes = append(changes, engine.Change{
				Action: action, Target: target + name, Detail: detail,
				Diff: udiff.Unified(pretty, "zakwas.yaml", redactURLs(from), redactURLs(next.text)),
				Apply: func(context.Context) error {
					if err := m.checkMCPFree(store, path, name, next.canon, isOwned); err != nil {
						return err
					}
					if err := store.set(path, name, server); err != nil {
						return err
					}
					return m.updateMCPState(func(st *mcpState) { st.record(provider, path, name, sum) })
				},
			})
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if writes {
		if err := checkMCPWritable(path); err != nil {
			return nil, fmt.Errorf("%s: %w", pretty, err)
		}
	}
	return changes, nil
}

func (m *Module) removeMCPChange(store mcpStore, path, name, hash string, cur mcpEntry) engine.Change {
	provider := store.provider()
	return engine.Change{
		Action: engine.Remove, Target: provider + " mcp " + name, Detail: "no longer in zakwas.yaml", Destructive: true,
		Diff: udiff.Unified(m.Paths.Pretty(path), "/dev/null", redactURLs(cur.text), ""),
		Apply: func(context.Context) error {
			have, err := store.load(path)
			if err != nil {
				return err
			}
			if e, ok := have[name]; ok && install.Sum([]byte(e.canon)) == hash {
				if err := store.unset(path, name); err != nil {
					return err
				}
			}
			return m.updateMCPState(func(st *mcpState) { st.record(provider, path, name, "") })
		},
	}
}

// checkMCPFree fails a create when a server of that name appeared since
// the plan, unless zakwas owns it or it is the one wanted.
func (m *Module) checkMCPFree(store mcpStore, path, name, canon string, owned bool) error {
	have, err := store.load(path)
	if err != nil {
		return err
	}
	if cur, ok := have[name]; ok && !owned && cur.canon != canon {
		return fmt.Errorf("server %q appeared in %s since the plan and differs from zakwas.yaml; apply again", name, m.Paths.Pretty(path))
	}
	return nil
}

func checkMCPWritable(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return errors.New("not a regular file")
	case info.Mode().Perm()&0o200 == 0:
		return errors.New("read-only; change it in the repo and apply it first")
	}
	return nil
}

func (m *Module) claudeJSONPath() string {
	if m.ClaudeConfigDir != "" {
		return filepath.Join(m.ClaudeConfigDir, ".claude.json")
	}
	return filepath.Join(m.Paths.Home, ".claude.json")
}

func (m *Module) claudeMCP() mcpStore { return claudeMCP{m: m} }

func (m *Module) codexMCP() mcpStore { return codexMCP{m: m} }

func (m *Module) copilotMCP() mcpStore { return copilotMCP{m: m} }
