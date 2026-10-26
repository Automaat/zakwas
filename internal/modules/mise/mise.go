// Package mise installs the pinned tool versions from the global mise config
// and optionally prunes versions no config references anymore.
package mise

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// Dir is where mise commands run. From $HOME, mise treats
// ~/.config/mise/config.toml as a project config that outranks the repo file
// passed in MISE_GLOBAL_CONFIG_FILE, so bumped pins would look installed
// until the files module had copied the new config there.
const Dir = "/"

type Module struct {
	Mise config.Mise
	// Layers are the mise configs local layers brought; their tools are
	// installed too.
	Layers []config.MiseConfig
	Paths  config.Paths
	Runner runner.Runner
	// ReleaseURL overrides where mise is downloaded from (tests).
	ReleaseURL string
}

func (m *Module) Name() string { return "mise" }

type tool struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

func (m *Module) cmd(args ...string) runner.Cmd {
	return m.cmdFor(m.Paths.Src(m.Mise.Config), args...)
}

// cmdFor runs mise with cfg as its only global config: MISE_GLOBAL_CONFIG_FILE
// also hides ~/.config/mise/conf.d, so each config is queried on its own.
func (m *Module) cmdFor(cfg string, args ...string) runner.Cmd {
	return runner.Cmd{
		Name: "mise",
		Args: args,
		Dir:  Dir,
		Env:  []string{"MISE_GLOBAL_CONFIG_FILE=" + cfg},
	}
}

// source is one mise config to install: the main one, then each layer's.
type source struct {
	path  string
	layer string
}

func (m *Module) sources() []source {
	out := []source{{path: m.Paths.Src(m.Mise.Config)}}
	for _, l := range m.Layers {
		out = append(out, source{path: l.Path, layer: l.Layer})
	}
	return out
}

// installChange runs `mise install` for one config. Output is passed through.
func (m *Module) installChange(src source) engine.Change {
	install := m.cmdFor(src.path, "install", "--yes")
	install.Stream = true
	c := engine.Change{
		Action: engine.Run, Target: "mise install", Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, install) },
	}
	if src.layer != "" {
		c.Detail = "layer " + src.layer
	}
	return c
}

func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	if !m.Runner.Installed("mise") {
		return m.planBootstrap(), nil
	}
	changes, err := m.planInstall(ctx)
	if err != nil {
		return nil, err
	}
	if !m.Mise.Prune {
		return changes, nil
	}
	prune, err := m.planPrune(ctx, len(changes) > 0)
	if err != nil {
		return nil, err
	}
	return mergeBumps(append(changes, prune...)), nil
}

// mergeBumps shows a tool whose only change is one new version installed and
// one old version pruned as a single `~ tool from → to`.
func mergeBumps(changes []engine.Change) []engine.Change {
	type pair struct{ add, remove []int }
	byTool := map[string]*pair{}
	for i, c := range changes {
		name, _, ok := strings.Cut(c.Target, "@")
		if !ok {
			continue
		}
		if byTool[name] == nil {
			byTool[name] = &pair{}
		}
		switch c.Action {
		case engine.Create:
			byTool[name].add = append(byTool[name].add, i)
		case engine.Remove:
			byTool[name].remove = append(byTool[name].remove, i)
		}
	}
	replace := map[int]engine.Change{}
	drop := map[int]bool{}
	for name, p := range byTool {
		if len(p.add) != 1 || len(p.remove) != 1 {
			continue
		}
		_, to, _ := strings.Cut(changes[p.add[0]].Target, "@")
		_, from, _ := strings.Cut(changes[p.remove[0]].Target, "@")
		replace[p.add[0]] = engine.Change{Action: engine.Update, Target: name, From: from, To: to}
		drop[p.remove[0]] = true
	}
	var out []engine.Change
	for i, c := range changes {
		switch {
		case drop[i]:
		case replace[i].Target != "":
			out = append(out, replace[i])
		default:
			out = append(out, c)
		}
	}
	return out
}

func (m *Module) planInstall(ctx context.Context) ([]engine.Change, error) {
	var missing []namedTool
	var installs []engine.Change
	for _, src := range m.sources() {
		tools, err := m.ls(ctx, src.path, "--global", "--missing")
		if err != nil {
			return nil, err
		}
		n := len(missing)
		for _, t := range tools {
			if !t.Installed {
				missing = append(missing, t)
			}
		}
		if len(missing) > n {
			installs = append(installs, m.installChange(src))
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(missing, func(a, b namedTool) int { return strings.Compare(a.Name, b.Name) })
	var changes []engine.Change
	for _, t := range missing {
		changes = append(changes, engine.Change{Action: engine.Create, Target: t.Name + "@" + t.Version})
	}
	return append(changes, installs...), nil
}

type namedTool struct {
	tool
	Name string
}

// ls returns the tool versions `mise ls` lists with cfg as the global
// config, sorted by name.
func (m *Module) ls(ctx context.Context, cfg string, flags ...string) ([]namedTool, error) {
	out, err := runner.Output(ctx, m.Runner, m.cmdFor(cfg, append(append([]string{"ls"}, flags...), "--json")...))
	if err != nil {
		return nil, err
	}
	var tools map[string][]tool
	if err := json.Unmarshal([]byte(out), &tools); err != nil {
		return nil, fmt.Errorf("mise ls: %w", err)
	}
	var res []namedTool
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		for _, t := range tools[name] {
			res = append(res, namedTool{tool: t, Name: name})
		}
	}
	return res, nil
}

// planPrune asks mise what it would prune now, but mise also counts the
// installed ~/.config/mise/config.toml, which still holds the old pins until
// the files module replaces it. So when tools are being installed or that
// copy is stale, prune is scheduled anyway: at apply time it runs after the
// new config is in place and removes the superseded versions in one pass.
func (m *Module) planPrune(ctx context.Context, installing bool) ([]engine.Change, error) {
	prunable, err := m.ls(ctx, m.Paths.Src(m.Mise.Config), "--prunable")
	if err != nil {
		return nil, err
	}
	stale, err := m.installedConfigStale()
	if err != nil {
		return nil, err
	}
	if len(prunable) == 0 && !installing && !stale {
		return nil, nil
	}
	var changes []engine.Change
	for _, t := range prunable {
		changes = append(changes, engine.Change{Action: engine.Remove, Target: t.Name + "@" + t.Version, Destructive: true})
	}
	detail := ""
	switch {
	case !installing && !stale:
	case len(prunable) == 0:
		detail = "versions superseded by the config update"
	default:
		detail = "and versions superseded by the config update"
	}
	prune := m.cmd("prune", "--yes")
	prune.Stream = true
	return append(changes, engine.Change{
		Action: engine.Run, Target: "mise prune", Detail: detail, Destructive: true, Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, prune) },
	}), nil
}

// InstalledConfig is where mise reads the user's global config; the files
// module keeps it in sync with the repo copy.
const InstalledConfig = "~/.config/mise/config.toml"

// installedConfigStale reports whether a config mise reads from home differs
// from its repo copy: the main one, and the copies of the layers' configs
// that their files entries install.
func (m *Module) installedConfigStale() (bool, error) {
	pairs := [][2]string{{m.Paths.Src(m.Mise.Config), m.Paths.Dst(InstalledConfig)}}
	for _, l := range m.Layers {
		for _, dst := range l.Installed {
			pairs = append(pairs, [2]string{l.Path, m.Paths.Dst(dst)})
		}
	}
	for _, p := range pairs {
		want, err := os.ReadFile(p[0])
		if err != nil {
			return false, err
		}
		have, err := os.ReadFile(p[1])
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !bytes.Equal(want, have) {
			return true, nil
		}
	}
	return false, nil
}
