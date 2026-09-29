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
	Mise   config.Mise
	Paths  config.Paths
	Runner runner.Runner
}

func (m *Module) Name() string { return "mise" }

type tool struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
}

func (m *Module) cmd(args ...string) runner.Cmd {
	return runner.Cmd{
		Name: "mise",
		Args: args,
		Dir:  Dir,
		Env:  []string{"MISE_GLOBAL_CONFIG_FILE=" + m.Paths.Src(m.Mise.Config)},
	}
}

func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
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
	return append(changes, prune...), nil
}

func (m *Module) planInstall(ctx context.Context) ([]engine.Change, error) {
	tools, err := m.ls(ctx, "--global", "--missing")
	if err != nil {
		return nil, err
	}
	var changes []engine.Change
	for _, t := range tools {
		if !t.Installed {
			changes = append(changes, engine.Change{Action: engine.Create, Target: t.Name + "@" + t.Version})
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	install := m.cmd("install", "--yes")
	install.Stream = true
	return append(changes, engine.Change{
		Action: engine.Run, Target: "mise install",
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, install) },
	}), nil
}

type namedTool struct {
	tool
	Name string
}

// ls returns the tool versions `mise ls` lists, sorted by name.
func (m *Module) ls(ctx context.Context, flags ...string) ([]namedTool, error) {
	out, err := runner.Output(ctx, m.Runner, m.cmd(append(append([]string{"ls"}, flags...), "--json")...))
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
	prunable, err := m.ls(ctx, "--prunable")
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
		changes = append(changes, engine.Change{Action: engine.Remove, Target: t.Name + "@" + t.Version})
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
		Action: engine.Run, Target: "mise prune", Detail: detail,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, prune) },
	}), nil
}

// InstalledConfig is where mise reads the user's global config; the files
// module keeps it in sync with the repo copy.
const InstalledConfig = "~/.config/mise/config.toml"

func (m *Module) installedConfigStale() (bool, error) {
	want, err := os.ReadFile(m.Paths.Src(m.Mise.Config))
	if err != nil {
		return false, err
	}
	have, err := os.ReadFile(m.Paths.Dst(InstalledConfig))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return !bytes.Equal(want, have), nil
}
