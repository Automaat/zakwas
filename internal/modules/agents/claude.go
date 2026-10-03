package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// claude converges Claude Code through `claude plugin … --json`. It never
// passes -y: a plugin whose marketplace declares an install command must be
// accepted by a person.
type claude struct {
	runner runner.Runner
	dir    string
}

const scopeUser = "user"

// cmd runs claude from / so a project's .claude settings in zakwas's working
// directory can't add marketplaces or plugins to what it sees.
func (c *claude) cmd(args ...string) runner.Cmd {
	return runner.Cmd{Name: "claude", Args: args, Dir: "/"}
}

type claudeMarketplace struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	Repo            string `json:"repo"`
	URL             string `json:"url"`
	Path            string `json:"path"`
	InstallLocation string `json:"installLocation"`
}

func (m claudeMarketplace) source() string {
	switch {
	case m.Repo != "":
		return m.Repo
	case m.URL != "":
		return m.URL
	}
	return m.Path
}

type claudeInstalled struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}

type claudeAvailable struct {
	PluginID string `json:"pluginId"`
	Version  string `json:"version"`
}

type claudeList struct {
	Installed []claudeInstalled `json:"installed"`
	Available []claudeAvailable `json:"available"`
}

// claudeResult is the line `claude plugin … --json` prints for a mutation.
type claudeResult struct {
	Outcome      string          `json:"outcome"`
	Message      string          `json:"message"`
	FailureCode  string          `json:"failureCode"`
	Marketplace  string          `json:"marketplace"`
	ShownCommand json.RawMessage `json:"shownCommand"`
}

type claudeState struct {
	marketplaces map[string]claudeMarketplace
	installed    []claudeInstalled
	available    map[string]claudeAvailable
}

func (c *claude) marketplaces(ctx context.Context) (map[string]claudeMarketplace, error) {
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "marketplace", "list", "--json"))
	if err != nil {
		return nil, err
	}
	var list []claudeMarketplace
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("claude plugin marketplace list: %w", err)
	}
	markets := map[string]claudeMarketplace{}
	for _, m := range list {
		markets[m.Name] = m
	}
	return markets, nil
}

func (c *claude) state(ctx context.Context) (claudeState, error) {
	st := claudeState{available: map[string]claudeAvailable{}}
	markets, err := c.marketplaces(ctx)
	if err != nil {
		return st, err
	}
	st.marketplaces = markets
	out, err := runner.Output(ctx, c.runner, c.cmd("plugin", "list", "--json", "--available"))
	if err != nil {
		return st, err
	}
	var list claudeList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return st, fmt.Errorf("claude plugin list: %w", err)
	}
	st.installed = list.Installed
	for _, a := range list.Available {
		st.available[a.PluginID] = a
	}
	return st, nil
}

func (c *claude) plan(ctx context.Context, d desired) ([]engine.Change, error) {
	if !c.runner.Installed("claude") {
		return nil, errors.New("claude: Claude Code is not installed (no claude on PATH); install it, or remove claude from agents.providers")
	}
	st, err := c.state(ctx)
	if err != nil {
		return nil, err
	}
	declared, err := c.userDeclared()
	if err != nil {
		return nil, err
	}
	auto, err := c.autoUpdate(declared)
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
		cur, ok := st.marketplaces[name]
		if !ok {
			changes = append(changes, engine.Change{
				Action: engine.Create, Target: "claude marketplace " + name, Detail: src + ", autoUpdate on",
				Apply: func(ctx context.Context) error { return c.addMarketplace(ctx, name, src) },
			})
			continue
		}
		if canonical(cur.source()) != canonical(src) {
			errs = append(errs, fmt.Errorf("claude: marketplace %q comes from %s, but zakwas.yaml declares %s; remove it with `claude plugin marketplace remove %s` and apply again", name, cur.source(), src, name))
			continue
		}
		if !auto[name] {
			changes = append(changes, engine.Change{
				Action: engine.Update, Target: "claude marketplace " + name, Detail: "turn on autoUpdate",
				Apply: func(context.Context) error { return c.setAutoUpdate(name) },
			})
		}
	}

	user := map[string]claudeInstalled{}
	anyScope := map[string]bool{}
	for _, p := range st.installed {
		anyScope[p.ID] = true
		if p.Scope == scopeUser {
			user[p.ID] = p
		}
	}
	for _, id := range d.plugins {
		name, mk, _ := config.SplitPluginID(id)
		inst, ok := user[id]
		if !ok {
			av, known := st.available[id]
			if _, added := st.marketplaces[mk]; added && !known && !anyScope[id] {
				errs = append(errs, fmt.Errorf("claude: plugin %q is not in marketplace %q", name, mk))
				continue
			}
			changes = append(changes, engine.Change{
				Action: engine.Create, Target: "claude plugin " + id, To: av.Version,
				Apply: func(ctx context.Context) error { return c.mutate(ctx, "install", id, "--scope", scopeUser) },
			})
			continue
		}
		if !inst.Enabled {
			changes = append(changes, engine.Change{
				Action: engine.Update, Target: "claude plugin " + id, Detail: "enable",
				Apply: func(ctx context.Context) error { return c.mutate(ctx, "enable", id, "--scope", scopeUser) },
			})
		}
		if !d.upgrade {
			continue
		}
		if latest := latestVersion(st.marketplaces[mk], name); latest != "" && latest != inst.Version {
			changes = append(changes, engine.Change{
				Action: engine.Update, Target: "claude plugin " + id, From: inst.Version, To: latest,
				Apply: func(ctx context.Context) error { return c.mutate(ctx, "update", id, "--scope", scopeUser) },
			})
		}
	}

	if d.prune {
		changes = append(changes, c.planPrune(d, st, user, declared)...)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return changes, nil
}

// planPrune removes undeclared user-scope plugins, then undeclared
// marketplaces. Project and local installs are never touched, and neither
// are the marketplaces they come from, nor plugins whose marketplace isn't
// configured (built-in or skills-dir ones zakwas can't declare). Only
// marketplaces declared in user settings are removed: one a project
// declares shows up in the list too, and `remove --scope user` refuses
// it.
func (c *claude) planPrune(d desired, st claudeState, user map[string]claudeInstalled, userDeclared map[string]autoUpdateEntry) []engine.Change {
	var changes []engine.Change
	ids := make([]string, 0, len(user))
	for id := range user {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if slices.Contains(d.plugins, id) {
			continue
		}
		_, mk, ok := config.SplitPluginID(id)
		if _, configured := st.marketplaces[mk]; !ok || !configured {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "claude plugin " + id, Detail: user[id].Version, Destructive: true,
			Apply: func(ctx context.Context) error { return c.mutate(ctx, "uninstall", id, "--scope", scopeUser) },
		})
	}
	inUse := map[string]bool{}
	for _, p := range st.installed {
		if _, mk, ok := config.SplitPluginID(p.ID); ok && p.Scope != scopeUser {
			inUse[mk] = true
		}
	}
	names := make([]string, 0, len(st.marketplaces))
	for n := range st.marketplaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		_, wanted := d.sources[name]
		_, ownedByUser := userDeclared[name]
		if wanted || inUse[name] || !ownedByUser {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "claude marketplace " + name, Detail: st.marketplaces[name].source(), Destructive: true,
			Apply: func(ctx context.Context) error {
				return c.mutate(ctx, "marketplace", "remove", name, "--scope", scopeUser)
			},
		})
	}
	return changes
}

// refresh updates the declared marketplaces that are already configured;
// apply adds missing ones fresh.
func (c *claude) refresh(ctx context.Context, d desired) error {
	if !c.runner.Installed("claude") {
		return nil
	}
	markets, err := c.marketplaces(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(d.sources))
	for n := range d.sources {
		if _, ok := markets[n]; ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var errs []error
	for _, n := range names {
		errs = append(errs, c.mutate(ctx, "marketplace", "update", n))
	}
	return errors.Join(errs...)
}

func (c *claude) addMarketplace(ctx context.Context, name, src string) error {
	res, err := c.run(ctx, "plugin", "marketplace", "add", src, "--scope", scopeUser, "--json")
	if err != nil {
		return err
	}
	if res.Marketplace != "" && res.Marketplace != name {
		return fmt.Errorf("claude: the marketplace at %s is named %q, not %q; use %q as its key in agents.marketplaces", src, res.Marketplace, name, res.Marketplace)
	}
	return c.setAutoUpdate(name)
}

// mutate runs `claude plugin <args> --json` and turns a failure into an
// error naming the plugin or marketplace.
func (c *claude) mutate(ctx context.Context, args ...string) error {
	_, err := c.run(ctx, append(append([]string{"plugin"}, args...), "--json")...)
	return err
}

func (c *claude) run(ctx context.Context, args ...string) (claudeResult, error) {
	cmd := c.cmd(args...)
	res, err := c.runner.Run(ctx, cmd)
	if err != nil {
		return claudeResult{}, err
	}
	out := parseResult(res.Stdout)
	if res.ExitCode == 0 {
		return out, nil
	}
	if len(out.ShownCommand) > 0 && string(out.ShownCommand) != "null" {
		return out, fmt.Errorf("%s: the marketplace declares a command that must be accepted first, and zakwas never accepts one; review it with `claude %s` (without --json) and run it yourself: %s",
			cmd, strings.Join(args[:len(args)-1], " "), shownCommand(out.ShownCommand))
	}
	if out.Message != "" {
		return out, fmt.Errorf("%s: %s", cmd, out.Message)
	}
	return out, res.Err(cmd)
}

// parseResult reads the last JSON line of stdout; tools may print progress
// before it.
func parseResult(stdout string) claudeResult {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r claudeResult
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &r) == nil && r.Outcome != "" {
			return r
		}
	}
	return claudeResult{}
}

func shownCommand(raw json.RawMessage) string {
	var v struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &v) == nil && v.Command != "" {
		return v.Command
	}
	return string(raw)
}

// latestVersion reads the version a marketplace offers for a plugin from
// its local manifest, preferring the plugin's own manifest for in-repo
// plugins. Plugins versioned only by commit report "", so they
// are never shown as outdated.
func latestVersion(m claudeMarketplace, plugin string) string {
	if m.InstallLocation == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(m.InstallLocation, ".claude-plugin", "marketplace.json"))
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
		if own := inRepoVersion(m.InstallLocation, p.Source); own != "" {
			return own
		}
		return p.Version
	}
	return ""
}

// inRepoVersion reads plugin.json of a plugin stored in the marketplace
// repo; Claude installs that version over the marketplace entry's.
func inRepoVersion(root string, source json.RawMessage) string {
	var rel string
	if json.Unmarshal(source, &rel) != nil || !strings.HasPrefix(rel, "./") {
		return ""
	}
	dir := filepath.Join(root, rel)
	if !strings.HasPrefix(dir, filepath.Clean(root)+string(filepath.Separator)) {
		return ""
	}
	var own struct {
		Version string `json:"version"`
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil || json.Unmarshal(data, &own) != nil {
		return ""
	}
	return own.Version
}

func (c *claude) knownPath() string {
	return filepath.Join(c.dir, "plugins", "known_marketplaces.json")
}
func (c *claude) settingsPath() string { return filepath.Join(c.dir, "settings.json") }

type autoUpdateEntry struct {
	AutoUpdate *bool `json:"autoUpdate"`
}

// autoUpdate reports which marketplaces auto-update. Claude keeps the flag
// in known_marketplaces.json, but a marketplace declared in user settings
// (as `marketplace add` does) takes it from there on startup, so both must
// say true.
func (c *claude) autoUpdate(declared map[string]autoUpdateEntry) (map[string]bool, error) {
	known, err := readEntries(c.knownPath())
	if err != nil {
		return nil, err
	}
	on := map[string]bool{}
	for name, e := range known {
		d, isDeclared := declared[name]
		on[name] = isTrue(e.AutoUpdate) && (!isDeclared || isTrue(d.AutoUpdate))
	}
	return on, nil
}

// userDeclared returns the marketplaces declared in user settings.
func (c *claude) userDeclared() (map[string]autoUpdateEntry, error) {
	settings, _, err := readObject(c.settingsPath())
	if err != nil {
		return nil, err
	}
	declared := map[string]autoUpdateEntry{}
	if raw, ok := settings.get("extraKnownMarketplaces"); ok {
		if err := json.Unmarshal(raw, &declared); err != nil {
			return nil, fmt.Errorf("%s: extraKnownMarketplaces: %w", c.settingsPath(), err)
		}
	}
	return declared, nil
}

func isTrue(b *bool) bool { return b != nil && *b }

func readEntries(path string) (map[string]autoUpdateEntry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries map[string]autoUpdateEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return entries, nil
}

// setAutoUpdate turns the flag on the way Claude's own /plugin toggle does:
// in known_marketplaces.json and, when declared there, in user settings.
func (c *claude) setAutoUpdate(name string) error {
	if err := setNested(c.knownPath(), name, true); err != nil {
		return err
	}
	settings, exists, err := readObject(c.settingsPath())
	if err != nil || !exists {
		return err
	}
	raw, ok := settings.get("extraKnownMarketplaces")
	if !ok {
		return nil
	}
	var declared object
	if err := json.Unmarshal(raw, &declared); err != nil {
		return fmt.Errorf("%s: extraKnownMarketplaces: %w", c.settingsPath(), err)
	}
	if _, ok := declared.get(name); !ok {
		return nil
	}
	if err := setField(&declared, name, "autoUpdate", true); err != nil {
		return err
	}
	if err := settings.set("extraKnownMarketplaces", declared); err != nil {
		return err
	}
	return writeObject(c.settingsPath(), settings)
}

// setNested sets file[name].autoUpdate, failing when the entry is missing.
func setNested(path, name string, v bool) error {
	file, exists, err := readObject(path)
	if err != nil {
		return err
	}
	if _, ok := file.get(name); !exists || !ok {
		return fmt.Errorf("claude: marketplace %q is not in %s", name, path)
	}
	if err := setField(&file, name, "autoUpdate", v); err != nil {
		return err
	}
	return writeObject(path, file)
}

func setField(o *object, entry, field string, v any) error {
	raw, _ := o.get(entry)
	var e object
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("%s: %w", entry, err)
	}
	if err := e.set(field, v); err != nil {
		return err
	}
	return o.set(entry, e)
}
