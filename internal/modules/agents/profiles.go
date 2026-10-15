package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/modules/templates"
)

// ProfilesStatePath is where zakwas records the hashes of the profile files
// it rendered under home.
func ProfilesStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "agent-profiles.json")
}

func (m *Module) profilesStatePath() string { return ProfilesStatePath(m.Paths.Home) }

// profileInstaller writes the profile files, with a state of its own apart
// from the files module's, which would remove them as orphans.
func (m *Module) profileInstaller() *install.Installer {
	if m.profiles == nil {
		m.profiles = &install.Installer{Paths: m.Paths, StatePath: m.profilesStatePath(), Immutable: m.Immutable}
	}
	return m.profiles
}

// orphanProfiles groups the removals of profile files nothing wants anymore.
const orphanProfiles = "profiles"

// profilePath is where a provider loads the profile from: Claude Code scans
// its agents dir recursively, so zakwas keeps its files in a subdirectory of
// their own; Codex reads <name>.config.toml from its home for --profile;
// Copilot reads agents/<name>.agent.md; opencode loads agents/<name>.md.
func (m *Module) profilePath(provider, name string) string {
	switch provider {
	case config.ProviderClaude:
		return filepath.Join(m.claudeBackend().dir, "agents", "zakwas", name+".md")
	case config.ProviderCodex:
		return filepath.Join(m.codexBackend().home, name+".config.toml")
	case config.ProviderCopilot:
		return filepath.Join(m.copilotBackend().home, "agents", name+".agent.md")
	}
	return m.Paths.Dst("~/.config/opencode/agents/" + name + ".md")
}

// planProfiles renders agents.profiles for every managed provider and
// removes the files zakwas rendered that no profile wants anymore. Claude,
// and codex, copilot and opencode when zakwas.yaml names them, are strict: a profile
// that can't be rendered or written fails that provider. opencode by the
// all-providers default is best effort: skipped when it isn't installed or
// nothing can be rendered, except that a name an opencode template defines
// fails the plan.
func (m *Module) planProfiles() (map[string][]engine.Change, map[string]error, error) {
	if len(m.Agents.Profiles) == 0 {
		if _, err := os.Lstat(m.profilesStatePath()); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if info, err := os.Lstat(m.profilesStatePath()); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("agents.profiles: %s is a symlink; remove it", m.Paths.Pretty(m.profilesStatePath()))
	}
	if err := install.CheckParents(m.Paths, m.profilesStatePath()); err != nil {
		return nil, nil, fmt.Errorf("agents.profiles: %w", err)
	}
	in := m.profileInstaller()
	tracked, err := in.Tracked()
	if err != nil {
		return nil, nil, err
	}
	byProvider := map[string][]engine.Change{}
	failed := map[string]error{}
	keep := map[string]bool{}
	for _, p := range config.Providers {
		list := m.Agents.ProfilesFor(p)
		if len(list) == 0 || !m.Agents.Manages(p) {
			continue
		}
		for _, prof := range list {
			keep[samePath(m.profilePath(p, prof.Name))] = true
		}
		strict := p == config.ProviderClaude || m.Agents.NamesInProfile(p)
		if !strict && !m.Runner.Installed(p) {
			continue
		}
		var errs []error
		var conflicts map[string]string
		if p == config.ProviderOpencode {
			var err error
			if conflicts, err = m.opencodeTemplateAgents(); err != nil {
				failed[p] = fmt.Errorf("%s: profiles: %w", p, err)
				continue
			}
		}
		var changes []engine.Change
		for _, prof := range list {
			dst := m.profilePath(p, prof.Name)
			if src, taken := conflicts[prof.Name]; taken {
				errs = append(errs, fmt.Errorf("profile %q is also defined by the opencode template %s under \"agent\"; rename one of them", prof.Name, m.Paths.Pretty(src)))
				continue
			}
			want, err := m.renderProfile(p, prof)
			if err != nil {
				if strict {
					errs = append(errs, fmt.Errorf("profile %q: %w", prof.Name, err))
				}
				continue
			}
			c, err := in.Plan(dst, want, 0o644)
			if err != nil {
				if strict {
					errs = append(errs, fmt.Errorf("profile %q: %w", prof.Name, err))
				}
				continue
			}
			if c == nil {
				continue
			}
			if c.Detail == "" {
				c.Detail = p + " profile " + prof.Name
			}
			c.Group = p
			changes = append(changes, *c)
		}
		if err := errors.Join(errs...); err != nil {
			failed[p] = fmt.Errorf("%s: profiles: %w", p, err)
			continue
		}
		byProvider[p] = changes
	}
	for _, dst := range tracked {
		if keep[samePath(dst)] {
			continue
		}
		c, err := m.planCopyRemove(in, m.profilesStatePath(), dst, "profile no longer declared")
		if err != nil {
			return nil, nil, err
		}
		c.Group = orphanProfiles
		byProvider[orphanProfiles] = append(byProvider[orphanProfiles], c)
	}
	return byProvider, failed, nil
}

// opencodeTemplateAgents maps the agent names the templates that render an
// opencode.json define to that template. A template that doesn't render is
// left to the templates module to report; one that renders to something
// that is not JSON, comments and trailing commas aside, fails the plan, since
// the names it defines can't be known.
func (m *Module) opencodeTemplateAgents() (map[string]string, error) {
	out := map[string]string{}
	for _, f := range m.TemplateFiles {
		if base := filepath.Base(f.Dst); base != "opencode.json" && base != "opencode.jsonc" {
			continue
		}
		src := filepath.Clean(m.Paths.Src(f.Src))
		data, err := templates.Render(src, templates.Data{Home: m.Paths.Home, Vars: m.Vars}, templates.Funcs(&m.Agents))
		if err != nil {
			continue
		}
		var doc struct {
			Agent map[string]json.RawMessage `json:"agent"`
		}
		if err := json.Unmarshal(stripJSONC(data), &doc); err != nil {
			return nil, fmt.Errorf("can't read the agents %s defines, to check them against the profiles: %w", m.Paths.Pretty(src), err)
		}
		for name := range doc.Agent {
			out[name] = src
		}
	}
	return out, nil
}

// stripJSONC drops the comments and trailing commas of JSONC, leaving
// strings alone.
func stripJSONC(data []byte) []byte {
	return scanJSONC(scanJSONC(data, false), true)
}

// scanJSONC copies data without its comments, or, with commas set, without
// the commas before a closing brace or bracket.
func scanJSONC(data []byte, commas bool) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(data) && data[j] != '"' {
				if data[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+1, len(data))
			out = append(out, data[i:end]...)
			i = end - 1
		case !commas && c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case !commas && c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return append(out, data[i:]...)
			}
			i += end + 3
		case commas && c == ',':
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// profileWarnings names, per provider and profile, the fields the provider
// can't express.
func (m *Module) profileWarnings() []string {
	var out []string
	for _, p := range config.Providers {
		if !m.Agents.Manages(p) {
			continue
		}
		for _, prof := range m.Agents.ProfilesFor(p) {
			if gaps := m.resolveProfile(p, prof).unsupported; len(gaps) > 0 {
				out = append(out, fmt.Sprintf("%s profile %s can't express: %s", p, prof.Name, strings.Join(gaps, "; ")))
			}
		}
	}
	return out
}
