// Package agents converges coding-agent plugins and marketplaces per
// provider. Claude Code, Codex, opencode and Copilot CLI have backends.
package agents

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
)

// Module converges the agents section. ClaudeConfigDir is an absolute
// $CLAUDE_CONFIG_DIR, or empty for Claude's default ~/.claude; CodexHome
// likewise for $CODEX_HOME and ~/.codex, and CopilotHome for $COPILOT_HOME
// and ~/.copilot.
type Module struct {
	Agents          config.Agents
	Paths           config.Paths
	Runner          runner.Runner
	ClaudeConfigDir string
	CodexHome       string
	CopilotHome     string
	// Guard is set when the zakwas.yaml has an agents section that leaves
	// the guard on: the managed agents are then kept from applying the
	// config, see config.Agents.Guard. TemplateSources are the absolute
	// paths of the templates, which opencode's share of the guard needs.
	Guard           bool
	TemplateSources []string
	// Vars are the templates.vars, which instructions can reference, and
	// Immutable is protect.immutable for the instructions copies.
	Vars      map[string]string
	Immutable bool
	// TemplateFiles are the templates, whose opencode.json can define agents
	// that agents.profiles must not repeat.
	TemplateFiles []config.TemplateFile
	installer     *install.Installer
	profiles      *install.Installer
	hooks         *install.Installer
	klaudiushPath string
}

func (m *Module) Name() string { return "agents" }

// backend converges one provider to its share of the agents section.
type backend interface {
	plan(ctx context.Context, d desired) ([]engine.Change, error)
	refresh(ctx context.Context, d desired) error
}

func (m *Module) backends() map[string]backend {
	return map[string]backend{
		config.ProviderClaude:   m.claudeBackend(),
		config.ProviderCodex:    m.codexBackend(),
		config.ProviderOpencode: m.opencodeBackend(),
		config.ProviderCopilot:  m.copilotBackend(),
	}
}

// copilotBackend passes an explicit home on to copilot, so its reads and
// zakwas's own match; the default is left implicit.
func (m *Module) copilotBackend() *copilot {
	if m.CopilotHome != "" {
		return &copilot{runner: m.Runner, home: m.CopilotHome, env: []string{"COPILOT_HOME=" + m.CopilotHome}}
	}
	return &copilot{runner: m.Runner, home: filepath.Join(m.Paths.Home, ".copilot")}
}

func (m *Module) codexBackend() *codex {
	if m.CodexHome != "" {
		return &codex{runner: m.Runner, home: m.CodexHome, env: []string{"CODEX_HOME=" + m.CodexHome}}
	}
	return &codex{runner: m.Runner, home: filepath.Join(m.Paths.Home, ".codex")}
}

// claudeBackend passes an explicit config dir on to claude, so its reads
// and zakwas's own match; the default is left implicit because setting it
// also moves Claude's global ~/.claude.json.
func (m *Module) claudeBackend() *claude {
	if m.ClaudeConfigDir != "" {
		return &claude{runner: m.Runner, dir: m.ClaudeConfigDir, env: []string{"CLAUDE_CONFIG_DIR=" + m.ClaudeConfigDir}}
	}
	return &claude{runner: m.Runner, dir: filepath.Join(m.Paths.Home, ".claude")}
}

// desired is what one provider should end up with; sources maps each
// marketplace name to its resolved source. named is set when zakwas.yaml
// lists the provider itself rather than getting it from the default, and
// explicit marks the plugins targeted at the provider by name.
type desired struct {
	sources  map[string]string
	plugins  []string
	explicit map[string]bool
	upgrade  bool
	prune    bool
	named    bool
}

// skip reports whether a provider has nothing to converge. Codex and
// Copilot are opt-in: a config that gets them only from the default
// providers predates their support and leaves them alone.
func (d desired) skip(provider string) bool {
	if config.OptIn(provider) && !d.named {
		return true
	}
	return len(d.sources) == 0 && len(d.plugins) == 0 && !d.prune
}

func (m *Module) desired(provider string) desired {
	d := desired{
		sources:  map[string]string{},
		explicit: map[string]bool{},
		upgrade:  m.Agents.Upgrade,
		prune:    m.Agents.Prune && m.Agents.Manages(provider),
		named:    m.Agents.Names(provider),
	}
	for _, name := range m.Agents.MarketplaceNames() {
		if slices.Contains(m.Agents.MarketplaceProviders(name), provider) {
			d.sources[name] = m.source(m.Agents.Marketplaces[name].Source)
		}
	}
	for _, p := range m.Agents.Plugins {
		if slices.Contains(m.Agents.PluginProviders(p), provider) {
			d.plugins = append(d.plugins, p.ID)
			d.explicit[p.ID] = m.Agents.NamesProvider(p, provider)
		}
	}
	return d
}

// Plan plans every provider with a backend, each provider's instructions
// copy first. A provider that fails to plan, its instructions included,
// plans nothing and doesn't hold back the others: their changes are
// returned along with the error.
func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	instructions, failed, err := m.planInstructions()
	errs := []error{err}
	commandHooks, hookFailed, hookErr := m.planCommandHooks()
	errs = append(errs, hookErr)
	permissions, permFailed, permErr := m.planPermissions()
	errs = append(errs, permErr)
	mcp, mcpFailed, mcpErr := m.planMCP()
	errs = append(errs, mcpErr)
	settings, settingsErr := m.planCodexSettings()
	profiles, profileFailed, profileErr := m.planProfiles()
	errs = append(errs, profileErr)
	skills, skillFailed, skillErr := m.planSkills()
	errs = append(errs, skillErr)
	var changes []engine.Change
	var klaudiushChange *engine.Change
	var klaudiushErr error
	if m.Agents.Hooks != nil && m.Agents.Hooks.Klaudiush {
		klaudiushChange, klaudiushErr = m.planKlaudiush()
		errs = append(errs, klaudiushErr)
	}
	backends := m.backends()
	for _, p := range append(slices.Clone(config.Providers), sharedInstructions) {
		if err := failed[p]; err != nil {
			errs = append(errs, err)
			delete(instructions, p)
			delete(profiles, p)
			delete(skills, p)
			continue
		}
		var providerSettingsErr error
		if p == config.ProviderCodex {
			providerSettingsErr = settingsErr
		}
		if err := errors.Join(hookFailed[p], permFailed[p], mcpFailed[p], profileFailed[p], skillFailed[p], providerSettingsErr); err != nil {
			errs = append(errs, err)
			delete(instructions, p)
			delete(profiles, p)
			delete(skills, p)
			continue
		}
		changes = append(changes, instructions[p]...)
		delete(instructions, p)
		changes = append(changes, commandHooks[p]...)
		changes = append(changes, permissions[p]...)
		changes = append(changes, mcp[p]...)
		if p == config.ProviderCodex {
			changes = append(changes, settings...)
		}
		changes = append(changes, profiles[p]...)
		delete(profiles, p)
		changes = append(changes, skills[p]...)
		delete(skills, p)
		b, ok := backends[p]
		if !ok {
			continue
		}
		d := m.desired(p)
		if d.skip(p) {
			continue
		}
		c, err := b.plan(ctx, d)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for i := range c {
			c[i].Group = p
		}
		changes = append(changes, c...)
	}
	for _, p := range slices.Sorted(maps.Keys(instructions)) {
		changes = append(changes, instructions[p]...)
	}
	for _, p := range slices.Sorted(maps.Keys(profiles)) {
		changes = append(changes, profiles[p]...)
	}
	for _, p := range slices.Sorted(maps.Keys(skills)) {
		changes = append(changes, skills[p]...)
	}
	if m.Agents.Hooks != nil && m.Agents.Hooks.Klaudiush && klaudiushChange == nil && len(changes) > 0 && klaudiushErr == nil {
		klaudiushChange = m.klaudiushFinalizeChange()
	}
	if klaudiushChange != nil {
		changes = append(changes, *klaudiushChange)
	}
	return changes, errors.Join(errs...)
}

// Refresh fetches new versions of the declared marketplaces, for `zakwas
// upgrade`; plan and apply never fetch.
func (m *Module) Refresh(ctx context.Context) error {
	var errs []error
	backends := m.backends()
	for _, p := range config.Providers {
		b, ok := backends[p]
		if !ok {
			continue
		}
		d := m.desired(p)
		if len(d.sources) == 0 || d.skip(p) {
			continue
		}
		errs = append(errs, b.refresh(ctx, d))
	}
	errs = append(errs, m.refreshSkills(ctx))
	return errors.Join(errs...)
}

// IsLocalSource reports whether a marketplace source is a filesystem path.
func IsLocalSource(src string) bool {
	return src == "." || src == ".." || src == "~" ||
		strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") ||
		strings.HasPrefix(src, "~/") || filepath.IsAbs(src)
}

// source resolves a local path against home and the config root; other
// sources are passed through.
func (m *Module) source(src string) string {
	if !IsLocalSource(src) {
		return src
	}
	if src == "~" || strings.HasPrefix(src, "~/") {
		return m.Paths.Dst(src)
	}
	return filepath.Clean(m.Paths.Src(src))
}

var (
	githubShort = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	githubURL   = regexp.MustCompile(`^(?:https?://|ssh://git@|git@)github\.com[/:]([^/]+/[^/]+?)(?:\.git)?/?$`)
)

// canonical maps the spellings of one source to the same key, so a
// marketplace added as "owner/repo" matches one declared by its GitHub URL.
func canonical(src string) string {
	if !IsLocalSource(src) {
		if base, ref, ok := strings.Cut(src, "#"); ok {
			return canonical(base) + "#" + ref
		}
	}
	switch {
	case IsLocalSource(src):
		if p, err := filepath.EvalSymlinks(src); err == nil {
			return p
		}
		return filepath.Clean(src)
	case githubShort.MatchString(src):
		return "github.com/" + strings.ToLower(strings.TrimSuffix(src, ".git"))
	}
	if g := githubURL.FindStringSubmatch(src); g != nil {
		return "github.com/" + strings.ToLower(g[1])
	}
	return strings.TrimSuffix(strings.TrimSuffix(src, "/"), ".git")
}
