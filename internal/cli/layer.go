package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/layers"
)

const layerUsage = `Usage: zakwas layer add NAME PATH [-c zakwas.yaml]
       zakwas layer remove NAME [-c zakwas.yaml]
       zakwas layer list [-c zakwas.yaml]

Registers a local layer for this machine: another zakwas.yaml (PATH, or a
directory holding one) merged into the config of the repo found by -c or the
working directory, in one plan and one state. The repo never refers to the
layer, and a config loaded another way gets none. A layer whose file goes
missing or invalid fails every plan, check and apply until it is fixed or
removed with 'zakwas layer remove', so its files are never pruned by mistake.

A layer runs shell commands and writes files like the main config does: only
register repos you control. add and remove can't run from an agent session.

Flags:
`

func runLayer(ctx context.Context, env Env, args []string, out, errOut *console) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fs, _ := newConfigFlags("layer", layerUsage, false, errOut)
		if len(args) == 0 {
			fs.Usage()
			return ExitUsage
		}
		fs.SetOutput(out)
		fs.Usage()
		return ExitOK
	}
	sub, rest := args[0], args[1:]
	arity, ok := map[string]int{"add": 2, "remove": 1, "list": 0}[sub]
	if !ok {
		errOut.printf("zakwas: unknown layer command %q; use add, remove or list\n", sub)
		return ExitUsage
	}
	fs, o := newConfigFlags("layer "+sub, layerUsage, false, errOut)
	pos, code, ok := parseSub(fs, out, rest)
	if !ok {
		return code
	}
	if len(pos) != arity {
		fs.Usage()
		return ExitUsage
	}
	path, err := configPath(o.cfgPath, env)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	cfg, err := config.LoadFor(path, env.Home, env.GOOS)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if sub == "list" {
		return listLayers(env, cfg, out, errOut)
	}
	if env.AgentSession != "" && cfg.Agents.GuardEnabled() {
		errOut.printf("zakwas: refusing to run layer %s from a %s session: a layer runs shell commands and writes files, so an agent that can register one can widen its own reach.\n"+
			"Run `zakwas layer %s` in a terminal outside the agent, or set agents.guard: false in %s\n", sub, env.AgentSession, sub, cfg.Path)
		return ExitErr
	}
	held, ok := takeLock(ctx, env, "layer", false, errOut)
	if !ok {
		return ExitErr
	}
	defer func() { _ = held.Release() }()
	reg, err := layers.Load(env.Home)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if sub == "add" {
		return addLayer(env, cfg, reg, pos[0], pos[1], out, errOut)
	}
	return removeLayer(env, cfg, reg, pos[0], out, errOut)
}

func addLayer(env Env, cfg *config.Config, reg *layers.Registry, name, path string, out, errOut *console) int {
	if !config.ValidLayerName(name) {
		errOut.printf("zakwas: layer name %q may only hold lowercase letters, digits and '-', and must start with a letter or digit\n", name)
		return ExitUsage
	}
	file, err := layerFile(env, path)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	current := reg.For(cfg.Root)
	if i := slices.IndexFunc(current, func(l config.Layer) bool { return l.Name == name }); i >= 0 {
		errOut.printf("zakwas: layer %q is already registered (%s); remove it first\n", name, current[i].Path)
		return ExitErr
	}
	next := slices.Concat(current, []config.Layer{{Name: name, Path: file}})
	if _, err := config.LoadLayered(cfg.Path, env.Home, env.GOOS, next); err != nil {
		var le *config.LayerError
		if errors.As(err, &le) && le.Layer.Name == name {
			errOut.print("zakwas: can't add " + le.Detail() + "\n")
		} else {
			errOut.fail(err)
		}
		return ExitErr
	}
	reg.Set(cfg.Root, next)
	if err := reg.Save(env.Home); err != nil {
		errOut.fail(err)
		return ExitErr
	}
	out.printf("Added layer %s (%s) to %s\nReview what it changes with: zakwas plan\n", name, file, cfg.Path)
	return ExitOK
}

func removeLayer(env Env, cfg *config.Config, reg *layers.Registry, name string, out, errOut *console) int {
	current := reg.For(cfg.Root)
	i := slices.IndexFunc(current, func(l config.Layer) bool { return l.Name == name })
	if i < 0 {
		errOut.printf("zakwas: no layer %q is registered for %s\n", name, cfg.Path)
		return ExitErr
	}
	reg.Set(cfg.Root, slices.Delete(current, i, i+1))
	if err := reg.Save(env.Home); err != nil {
		errOut.fail(err)
		return ExitErr
	}
	out.printf("Removed layer %s from %s\nThe next plan lists what only it installed as removals: zakwas plan\n", name, cfg.Path)
	return ExitOK
}

func listLayers(env Env, cfg *config.Config, out, errOut *console) int {
	reg, err := layers.Load(env.Home)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	registered := reg.For(cfg.Root)
	if len(registered) == 0 {
		out.printf("No layers registered for %s\n", cfg.Path)
		return ExitOK
	}
	for _, l := range registered {
		note := ""
		if _, err := os.Stat(l.Path); err != nil {
			note = "  (missing)"
		}
		out.printf("%s\t%s%s\n", l.Name, l.Path, note)
	}
	return ExitOK
}

// layerFile resolves the PATH given to layer add to the layer's zakwas.yaml:
// absolute, with symlinks followed, so the registry holds one spelling.
func layerFile(env Env, path string) (string, error) {
	path = config.Paths{Home: env.Home}.Dst(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(env.Cwd, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		path = filepath.Join(path, config.FileName)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(real); err != nil || info.IsDir() {
		return "", fmt.Errorf("%s is not a file", real)
	}
	return real, nil
}

// registeredLayers lists the layers registered for the root of the config at
// path, none for a root that has none. A file that is itself registered as a
// layer of another root is refused: loaded alone it would plan the removal of
// everything the main config installed.
func registeredLayers(path, home string) ([]config.Layer, error) {
	root, err := config.RootOf(path)
	if err != nil {
		return nil, err
	}
	reg, err := layers.Load(home)
	if err != nil {
		return nil, err
	}
	if main, layer, ok := reg.LayerOf(path); ok && !samePath(main, root) {
		return nil, fmt.Errorf("%s is layer %q of %s: run zakwas there, or pass -c %s", path, layer.Name, filepath.Join(main, config.FileName), filepath.Join(main, config.FileName))
	}
	return reg.For(root), nil
}

func samePath(a, b string) bool {
	x, errA := os.Stat(a)
	y, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(x, y)
}
