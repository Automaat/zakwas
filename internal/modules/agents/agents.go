// Package agents converges coding-agent plugins and marketplaces per
// provider. Only Claude Code has a backend so far; providers without one
// are skipped.
package agents

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// Module converges the agents section. ClaudeConfigDir is an absolute
// $CLAUDE_CONFIG_DIR, or empty for Claude's default ~/.claude.
type Module struct {
	Agents          config.Agents
	Paths           config.Paths
	Runner          runner.Runner
	ClaudeConfigDir string
}

func (m *Module) Name() string { return "agents" }

// backend converges one provider to its share of the agents section.
type backend interface {
	plan(ctx context.Context, d desired) ([]engine.Change, error)
	refresh(ctx context.Context, d desired) error
}

func (m *Module) backends() map[string]backend {
	return map[string]backend{
		config.ProviderClaude: m.claudeBackend(),
	}
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
// marketplace name to its resolved source.
type desired struct {
	sources map[string]string
	plugins []string
	upgrade bool
	prune   bool
}

func (d desired) empty() bool {
	return len(d.sources) == 0 && len(d.plugins) == 0 && !d.prune
}

func (m *Module) desired(provider string) desired {
	d := desired{
		sources: map[string]string{},
		upgrade: m.Agents.Upgrade,
		prune:   m.Agents.Prune && m.Agents.Manages(provider),
	}
	for _, name := range m.Agents.MarketplaceNames() {
		if slices.Contains(m.Agents.MarketplaceProviders(name), provider) {
			d.sources[name] = m.source(m.Agents.Marketplaces[name].Source)
		}
	}
	for _, p := range m.Agents.Plugins {
		if slices.Contains(m.Agents.PluginProviders(p), provider) {
			d.plugins = append(d.plugins, p.ID)
		}
	}
	return d
}

// Plan plans every provider with a backend; codex and opencode have none
// yet and are skipped.
func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	var errs []error
	backends := m.backends()
	for _, p := range config.Providers {
		b, ok := backends[p]
		if !ok {
			continue
		}
		d := m.desired(p)
		if d.empty() {
			continue
		}
		c, err := b.plan(ctx, d)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		changes = append(changes, c...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return changes, nil
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
		if len(d.sources) == 0 {
			continue
		}
		errs = append(errs, b.refresh(ctx, d))
	}
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
