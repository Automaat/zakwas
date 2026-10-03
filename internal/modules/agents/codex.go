package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// codex converges Codex through `codex plugin … --json`. Codex has no
// plugin update command: `codex plugin add` installs the marketplace's
// current version over the installed one and re-enables a disabled plugin.
type codex struct {
	runner runner.Runner
	home   string
	env    []string
}

// cmd runs codex from / so a project's marketplace in zakwas's working
// directory can't add to what it sees.
func (c *codex) cmd(args ...string) runner.Cmd {
	return runner.Cmd{Name: "codex", Args: args, Dir: "/", Env: c.env}
}

func (c *codex) configPath() string { return filepath.Join(c.home, "config.toml") }

type codexMarketplace struct {
	Name string `json:"name"`
	Root string `json:"root"`
}

type codexPlugin struct {
	PluginID string `json:"pluginId"`
	Version  string `json:"version"`
	Enabled  bool   `json:"enabled"`
}

// codexSource is a marketplace added with `codex plugin marketplace add`,
// as Codex records it in config.toml.
type codexSource struct {
	SourceType string `toml:"source_type"`
	Source     string `toml:"source"`
	Ref        string `toml:"ref"`
}

func (s codexSource) source() string {
	if s.Ref != "" {
		return s.Source + "#" + s.Ref
	}
	return s.Source
}

// codexState: listed holds every marketplace Codex considers, configured
// only those added to config.toml, which are the ones zakwas may own. The
// rest (a personal ~/.agents marketplace, remote ones) are never removed.
type codexState struct {
	listed     map[string]codexMarketplace
	configured map[string]codexSource
	installed  []codexPlugin
	available  map[string]codexPlugin
}

func (c *codex) configured() (map[string]codexSource, error) {
	data, err := os.ReadFile(c.configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]codexSource{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Marketplaces map[string]codexSource `toml:"marketplaces"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", c.configPath(), err)
	}
	if cfg.Marketplaces == nil {
		cfg.Marketplaces = map[string]codexSource{}
	}
	return cfg.Marketplaces, nil
}

func (c *codex) state(ctx context.Context) (codexState, error) {
	st := codexState{listed: map[string]codexMarketplace{}, available: map[string]codexPlugin{}}
	configured, err := c.configured()
	if err != nil {
		return st, err
	}
	st.configured = configured
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "marketplace", "list", "--json"))
	if err != nil {
		return st, errors.Join(err, brokenLocal(configured))
	}
	var markets struct {
		Marketplaces []codexMarketplace `json:"marketplaces"`
	}
	if err := json.Unmarshal([]byte(out), &markets); err != nil {
		return st, fmt.Errorf("codex plugin marketplace list: %w", err)
	}
	for _, m := range markets.Marketplaces {
		st.listed[m.Name] = m
	}
	out, err = runner.Output(ctx, c.runner, c.cmd("plugin", "list", "--available", "--json"))
	if err != nil {
		return st, err
	}
	var plugins struct {
		Installed []codexPlugin `json:"installed"`
		Available []codexPlugin `json:"available"`
	}
	if err := json.Unmarshal([]byte(out), &plugins); err != nil {
		return st, fmt.Errorf("codex plugin list: %w", err)
	}
	st.installed = plugins.Installed
	for _, p := range plugins.Available {
		st.available[p.PluginID] = p
	}
	return st, nil
}

// brokenLocal names local marketplaces in config.toml whose directory has
// no manifest, to explain a failed list: Codex then fails every list
// command, prune included, until they are removed by hand.
func brokenLocal(configured map[string]codexSource) error {
	names := make([]string, 0, len(configured))
	for n := range configured {
		names = append(names, n)
	}
	sort.Strings(names)
	var errs []error
	for _, n := range names {
		s := configured[n]
		if s.SourceType != "local" {
			continue
		}
		if !hasAnyManifest(s.Source) {
			errs = append(errs, fmt.Errorf("codex: local marketplace %q at %s has no marketplace manifest, so codex can't list plugins; restore it or run `codex plugin marketplace remove %s`", n, s.Source, n))
		}
	}
	return errors.Join(errs...)
}

// hasAnyManifest reports whether root has any manifest Codex 0.160 loads.
func hasAnyManifest(root string) bool {
	if codexManifest(root) != "" {
		return true
	}
	for _, rel := range []string{filepath.Join(".agents", "plugins", "api_marketplace.json"), filepath.Join(".cursor-plugin", "marketplace.json")} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return true
		}
	}
	return false
}

// codexManifest returns the plugin manifest Codex reads in a marketplace
// root, or "" when there is none.
func codexManifest(root string) string {
	for _, rel := range []string{filepath.Join(".agents", "plugins", "marketplace.json"), filepath.Join(".claude-plugin", "marketplace.json")} {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (c *codex) plan(ctx context.Context, d desired) ([]engine.Change, error) {
	if !c.runner.Installed("codex") {
		return nil, errors.New("codex: Codex is not installed (no codex on PATH); install it, or remove codex from agents.providers")
	}
	st, err := c.state(ctx)
	if err != nil {
		return nil, err
	}
	var changes []engine.Change
	var errs []error

	names := make([]string, 0, len(d.sources))
	for n := range d.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		src := d.sources[name]
		cur, ok := st.configured[name]
		switch {
		case ok && canonical(cur.source()) != canonical(src):
			errs = append(errs, fmt.Errorf("codex: marketplace %q comes from %s, but zakwas.yaml declares %s; remove it with `codex plugin marketplace remove %s` and apply again", name, cur.source(), src, name))
		case ok:
		case st.listed[name].Name != "":
			errs = append(errs, fmt.Errorf("codex: a marketplace named %q is already loaded from %s, outside config.toml; give the one in zakwas.yaml another key, matching its manifest name", name, st.listed[name].Root))
		default:
			changes = append(changes, engine.Change{
				Action: engine.Create, Target: "codex marketplace " + name, Detail: src,
				Apply: func(ctx context.Context) error { return c.addMarketplace(ctx, name, src) },
			})
		}
	}

	installed := map[string]codexPlugin{}
	for _, p := range st.installed {
		installed[p.PluginID] = p
	}
	for _, id := range d.plugins {
		name, mk, _ := config.SplitPluginID(id)
		add := func(ctx context.Context) error { return c.mutate(ctx, "add", id) }
		inst, ok := installed[id]
		if !ok {
			av, known := st.available[id]
			if _, listed := st.listed[mk]; listed && !known {
				errs = append(errs, fmt.Errorf("codex: plugin %q is not in marketplace %q", name, mk))
				continue
			}
			changes = append(changes, engine.Change{Action: engine.Create, Target: "codex plugin " + id, To: av.Version, Apply: add})
			continue
		}
		var latest string
		if d.upgrade || !inst.Enabled {
			latest = codexLatestVersion(st.listed[mk].Root, name)
		}
		newer := newerVersion(latest, inst.Version)
		switch {
		case !inst.Enabled && newer:
			changes = append(changes, engine.Change{Action: engine.Update, Target: "codex plugin " + id, Detail: "enable", From: inst.Version, To: latest, Apply: add})
		case !inst.Enabled:
			changes = append(changes, engine.Change{Action: engine.Update, Target: "codex plugin " + id, Detail: "enable", Apply: add})
		case newer:
			changes = append(changes, engine.Change{Action: engine.Update, Target: "codex plugin " + id, From: inst.Version, To: latest, Apply: add})
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

// planPrune removes undeclared plugins of marketplaces in config.toml, then
// those marketplaces. Plugins go first: Codex stops listing a plugin once
// its marketplace is gone, leaving it installed. Marketplaces Codex finds
// on its own and their plugins are never touched.
func (c *codex) planPrune(d desired, st codexState) []engine.Change {
	var changes []engine.Change
	plugins := slices.Clone(st.installed)
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].PluginID < plugins[j].PluginID })
	for _, p := range plugins {
		id := p.PluginID
		if slices.Contains(d.plugins, id) {
			continue
		}
		_, mk, ok := config.SplitPluginID(id)
		if _, configured := st.configured[mk]; !ok || !configured {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "codex plugin " + id, Detail: p.Version, Destructive: true,
			Apply: func(ctx context.Context) error { return c.mutate(ctx, "remove", id) },
		})
	}
	names := make([]string, 0, len(st.configured))
	for n := range st.configured {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, wanted := d.sources[name]; wanted {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "codex marketplace " + name, Detail: st.configured[name].source(), Destructive: true,
			Apply: func(ctx context.Context) error { return c.mutate(ctx, "marketplace", "remove", name) },
		})
	}
	return changes
}

// refresh upgrades the declared Git marketplaces already in config.toml;
// local ones are read in place and can't be upgraded. Codex moves installed
// plugins of an upgraded marketplace to the new snapshot on its own.
func (c *codex) refresh(ctx context.Context, d desired) error {
	if !c.runner.Installed("codex") {
		return nil
	}
	configured, err := c.configured()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(d.sources))
	var errs []error
	for n, src := range d.sources {
		cur, ok := configured[n]
		switch {
		case !ok, cur.SourceType != "git":
		case canonical(cur.source()) != canonical(src):
			errs = append(errs, fmt.Errorf("codex: marketplace %q comes from %s, but zakwas.yaml declares %s; not refreshing it", n, cur.source(), src))
		default:
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		errs = append(errs, c.mutate(ctx, "marketplace", "upgrade", n))
	}
	return errors.Join(errs...)
}

// addMarketplace passes a "#ref" suffix as --ref, the way Codex takes it.
func (c *codex) addMarketplace(ctx context.Context, name, src string) error {
	args := []string{"plugin", "marketplace", "add", src}
	if !IsLocalSource(src) {
		if base, ref, ok := strings.Cut(src, "#"); ok {
			args = []string{"plugin", "marketplace", "add", base, "--ref", ref}
		}
	}
	out, err := c.run(ctx, append(args, "--json")...)
	if err != nil {
		return err
	}
	var res struct {
		MarketplaceName string `json:"marketplaceName"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return fmt.Errorf("codex plugin marketplace add: %w", err)
	}
	if res.MarketplaceName != name {
		return fmt.Errorf("codex: the marketplace at %s is named %q, not %q; use %q as its key in agents.marketplaces", src, res.MarketplaceName, name, res.MarketplaceName)
	}
	return nil
}

func (c *codex) mutate(ctx context.Context, args ...string) error {
	_, err := c.run(ctx, append(append([]string{"plugin"}, args...), "--json")...)
	return err
}

// run returns stdout; Codex reports failures as text on stderr even with
// --json.
func (c *codex) run(ctx context.Context, args ...string) (string, error) {
	return runner.Output(ctx, c.runner, c.cmd(args...))
}

// codexLatestVersion reads the version Codex would install for a plugin from
// the marketplace snapshot at root, the way Codex picks it: the
// .agents/plugins manifest over the .claude-plugin one, then
// .codex-plugin/plugin.json over .claude-plugin/plugin.json over the
// manifest entry. Sources without a local dir (git, github, ...) use the
// manifest entry. Plugins missing from the manifest report "", so they are
// never shown as outdated.
func codexLatestVersion(root, plugin string) string {
	if root == "" {
		return ""
	}
	if path := codexManifest(root); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		var manifest struct {
			Plugins []struct {
				Name    string          `json:"name"`
				Version string          `json:"version"`
				Source  json.RawMessage `json:"source"`
			} `json:"plugins"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return ""
		}
		for _, p := range manifest.Plugins {
			if p.Name != plugin {
				continue
			}
			if dir, ok := codexPluginDir(root, p.Source); ok {
				for _, m := range []string{".codex-plugin", ".claude-plugin"} {
					if v := pluginJSONVersion(filepath.Join(dir, m, "plugin.json")); v != "" {
						return v
					}
				}
			}
			return p.Version
		}
		return ""
	}
	return ""
}

// codexPluginDir resolves a local plugin source, "./dir" or
// {"source": "local", "path": "./dir"}, inside root.
func codexPluginDir(root string, source json.RawMessage) (string, bool) {
	var rel string
	if json.Unmarshal(source, &rel) != nil {
		var obj struct {
			Source string `json:"source"`
			Path   string `json:"path"`
		}
		if json.Unmarshal(source, &obj) != nil || obj.Source != "local" {
			return "", false
		}
		rel = obj.Path
	}
	if !strings.HasPrefix(rel, "./") {
		return "", false
	}
	clean := filepath.Clean(root)
	dir := filepath.Join(clean, rel)
	if dir != clean && !strings.HasPrefix(dir, clean+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}

func pluginJSONVersion(path string) string {
	var own struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &own) != nil {
		return ""
	}
	return own.Version
}
