package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// copilot converges GitHub Copilot CLI through `copilot plugin … --json`.
// Copilot reads Claude-format marketplaces. zakwas only reads its
// settings.json, to see the ref of a marketplace; copilot itself writes
// that file and config.json, and zakwas never does.
type copilot struct {
	runner runner.Runner
	home   string
	env    []string
}

// cmd runs copilot from / so a project's plugin settings in zakwas's
// working directory can't add to what it sees.
func (c *copilot) cmd(args ...string) runner.Cmd {
	return runner.Cmd{Name: "copilot", Args: args, Dir: "/", Env: c.env}
}

func (c *copilot) settingsPath() string { return filepath.Join(c.home, "settings.json") }

// copilotMarketplace is one `plugin marketplace list --json` entry. Its
// source reads "GitHub: owner/repo", "URL: https://…" or "Local: /path",
// without the ref the marketplace was added with.
type copilotMarketplace struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	IsDefault bool   `json:"isDefault"`
}

var copilotSourcePrefix = regexp.MustCompile(`^(?:GitHub|URL|Git|Local): `)

func (m copilotMarketplace) source() string {
	return copilotSourcePrefix.ReplaceAllString(m.Source, "")
}

// copilotPlugin is one `plugin list --json` entry. Copilot reports a
// version for plugins it loads live from a local marketplace only.
type copilotPlugin struct {
	Name        string `json:"name"`
	Marketplace string `json:"marketplace"`
	Version     string `json:"version"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
}

const copilotLive = "live"

func (p copilotPlugin) id() string {
	if p.Marketplace == "" {
		return p.Name
	}
	return p.Name + "@" + p.Marketplace
}

type copilotState struct {
	marketplaces map[string]copilotMarketplace
	installed    []copilotPlugin
}

func (c *copilot) marketplaces(ctx context.Context) (map[string]copilotMarketplace, error) {
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "marketplace", "list", "--json"))
	if err != nil {
		return nil, err
	}
	var list []copilotMarketplace
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("copilot plugin marketplace list: %w", err)
	}
	markets := map[string]copilotMarketplace{}
	for _, m := range list {
		markets[m.Name] = m
	}
	return markets, nil
}

func (c *copilot) state(ctx context.Context) (copilotState, error) {
	var st copilotState
	markets, err := c.marketplaces(ctx)
	if err != nil {
		return st, err
	}
	st.marketplaces = markets
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "list", "--json"))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal([]byte(out), &st.installed); err != nil {
		return st, fmt.Errorf("copilot plugin list: %w", err)
	}
	return st, nil
}

// refs reads the ref each marketplace was added with from Copilot's
// settings.json, which the list omits. A marketplace added without a ref
// has an entry with an empty ref; one without an entry, or a missing or
// unreadable file, has no known ref and is left out.
func (c *copilot) refs() map[string]string {
	data, err := os.ReadFile(c.settingsPath())
	if err != nil {
		return nil
	}
	var settings struct {
		Marketplaces map[string]json.RawMessage `json:"extraKnownMarketplaces"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return nil
	}
	refs := map[string]string{}
	for name, raw := range settings.Marketplaces {
		var m struct {
			Source struct {
				Ref string `json:"ref"`
			} `json:"source"`
		}
		if json.Unmarshal(raw, &m) == nil && string(raw) != "null" {
			refs[name] = m.Source.Ref
		}
	}
	return refs
}

// splitRef separates the "#ref" of a declared remote source.
func splitRef(src string) (base, ref string) {
	if IsLocalSource(src) {
		return src, ""
	}
	base, ref, _ = strings.Cut(src, "#")
	return base, ref
}

// differs reports whether the added marketplace cur is not the declared
// src: another source, or another ref than the one settings.json records
// for it. A declared ref counts against an entry with no ref, which was
// added at the default branch; a marketplace with no entry is not judged
// by ref.
func (cur copilotMarketplace) differs(refs map[string]string, src string) bool {
	base, ref := splitRef(src)
	if canonical(cur.source()) != canonical(base) {
		return true
	}
	added, known := refs[cur.Name]
	return known && ref != "" && added != ref
}

func (cur copilotMarketplace) recorded(refs map[string]string, src string) string {
	added, known := refs[cur.Name]
	switch {
	case !known:
		return cur.source()
	case added != "":
		return cur.source() + "#" + added
	}
	if _, ref := splitRef(src); ref != "" {
		return cur.source() + " (added without a ref)"
	}
	return cur.source()
}

func (c *copilot) plan(ctx context.Context, d desired) ([]engine.Change, error) {
	if !c.runner.Installed("copilot") {
		return nil, errors.New("copilot: Copilot CLI is not installed (no copilot on PATH); install it, or remove copilot from agents.providers")
	}
	st, err := c.state(ctx)
	if err != nil {
		return nil, err
	}
	refs := c.refs()
	var changes []engine.Change
	var errs []error

	names := make([]string, 0, len(d.sources))
	for n := range d.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		src := d.sources[name]
		cur, ok := st.marketplaces[name]
		if !ok {
			changes = append(changes, engine.Change{
				Action: engine.Create, Target: "copilot marketplace " + name, Detail: src,
				Apply: func(ctx context.Context) error { return c.addMarketplace(ctx, name, src) },
			})
			continue
		}
		if cur.differs(refs, src) {
			errs = append(errs, fmt.Errorf("copilot: marketplace %q comes from %s, but zakwas.yaml declares %s; remove it with `copilot plugin marketplace remove %s` (add --force when its plugins are installed) and apply again", name, cur.recorded(refs, src), src, name))
		}
	}

	installed := map[string]copilotPlugin{}
	for _, p := range st.installed {
		installed[p.id()] = p
	}
	for _, id := range d.plugins {
		inst, ok := installed[id]
		if !ok {
			changes = append(changes, engine.Change{
				Action: engine.Create, Target: "copilot plugin " + id,
				Apply: func(ctx context.Context) error { return c.mutate(ctx, "install", id) },
			})
			continue
		}
		if !inst.Enabled {
			changes = append(changes, engine.Change{
				Action: engine.Update, Target: "copilot plugin " + id, Detail: "enable",
				Apply: func(ctx context.Context) error { return c.mutate(ctx, "enable", id) },
			})
		}
	}

	if d.prune {
		changes = append(changes, c.planPrune(d, st)...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return changes, nil
}

// planPrune uninstalls undeclared plugins of non-default marketplaces, then
// removes the undeclared non-default marketplaces nothing else uses.
// Copilot's two built-in marketplaces and their plugins are never touched,
// nor are plugins installed straight from a repository. Copilot keeps an
// uninstalled live plugin of a local marketplace in its list, disabled; it
// counts as gone.
func (c *copilot) planPrune(d desired, st copilotState) []engine.Change {
	var changes []engine.Change
	plugins := slices.Clone(st.installed)
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].id() < plugins[j].id() })
	removed := map[string]bool{}
	for _, p := range plugins {
		id := p.id()
		mk, listed := st.marketplaces[p.Marketplace]
		if slices.Contains(d.plugins, id) || p.Marketplace == "" || !listed || mk.IsDefault {
			continue
		}
		removed[id] = true
		if p.Source == copilotLive && !p.Enabled {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "copilot plugin " + id, Detail: p.Version, Destructive: true,
			Apply: func(ctx context.Context) error { return c.mutate(ctx, "uninstall", id) },
		})
	}
	inUse := map[string]bool{}
	for _, p := range st.installed {
		if p.Marketplace != "" && !removed[p.id()] {
			inUse[p.Marketplace] = true
		}
	}
	names := make([]string, 0, len(st.marketplaces))
	for n := range st.marketplaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		_, wanted := d.sources[name]
		if wanted || inUse[name] || st.marketplaces[name].IsDefault {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "copilot marketplace " + name, Detail: st.marketplaces[name].source(), Destructive: true,
			Apply: func(ctx context.Context) error { return c.mutate(ctx, "marketplace", "remove", name) },
		})
	}
	return changes
}

// refresh updates the declared marketplaces that are already added, and
// with upgrade the declared plugins installed from them: Copilot lists no
// version for an installed plugin, so the plan can't tell an outdated one
// and `plugin update` is the only way to bring it up to date.
func (c *copilot) refresh(ctx context.Context, d desired) error {
	if !c.runner.Installed("copilot") {
		return nil
	}
	st, err := c.state(ctx)
	if err != nil {
		return err
	}
	refs := c.refs()
	names := make([]string, 0, len(d.sources))
	var errs []error
	for n, src := range d.sources {
		cur, ok := st.marketplaces[n]
		switch {
		case !ok:
		case cur.differs(refs, src):
			errs = append(errs, fmt.Errorf("copilot: marketplace %q comes from %s, but zakwas.yaml declares %s; not refreshing it", n, cur.recorded(refs, src), src))
		default:
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		errs = append(errs, c.mutate(ctx, "marketplace", "update", n))
	}
	if d.upgrade {
		for _, p := range st.installed {
			if id := p.id(); slices.Contains(d.plugins, id) && p.Enabled && p.Source != copilotLive && slices.Contains(names, p.Marketplace) {
				errs = append(errs, c.mutate(ctx, "update", id))
			}
		}
	}
	return errors.Join(errs...)
}

var copilotMarketplaceAdded = regexp.MustCompile(`Marketplace "([^"]+)" added`)

func (c *copilot) addMarketplace(ctx context.Context, name, src string) error {
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "marketplace", "add", src))
	if err != nil {
		return err
	}
	if g := copilotMarketplaceAdded.FindStringSubmatch(out); g != nil && g[1] != name {
		return fmt.Errorf("copilot: the marketplace at %s is named %q, not %q; use %q as its key in agents.marketplaces", src, g[1], name, g[1])
	}
	return nil
}

// mutate runs `copilot plugin <args>`; Copilot reports a failure as text
// on stderr with a non-zero exit.
func (c *copilot) mutate(ctx context.Context, args ...string) error {
	return runner.Check(ctx, c.runner, c.cmd(append([]string{"plugin"}, args...)...))
}
