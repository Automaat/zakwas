package cli

import (
	"context"
	"errors"
	"os"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/modules/launchd"
)

var windowsUnsupported = map[string]string{
	"system":   "the system module is macOS only, so it does not run on Windows",
	"brew":     "the brew module is macOS only, so it does not run on Windows",
	"defaults": "the defaults module is macOS only, so it does not run on Windows",
	"launchd":  "the launchd module is macOS only, so it does not run on Windows",
	"mise":     "the mise module is not supported on Windows yet",
	"commands": "the commands module runs sh snippets and is not supported on Windows yet",
}

type unsupportedModule struct {
	name, reason string
}

func (m unsupportedModule) Name() string { return m.name }

func (m unsupportedModule) Plan(context.Context) ([]engine.Change, error) {
	return nil, errors.New(m.name + ": " + m.reason)
}

// forPlatform swaps the modules goos can't run for ones whose plan fails
// with a clear message, and drops those that have nothing configured, so a
// config without them plans cleanly. Only Windows restricts modules.
func forPlatform(goos string, cfg *config.Config, env Env, mods []engine.Module) []engine.Module {
	if goos != "windows" {
		return mods
	}
	out := make([]engine.Module, 0, len(mods))
	for _, m := range mods {
		reason, bad := windowsUnsupported[m.Name()]
		switch {
		case !bad:
			out = append(out, m)
		case moduleConfigured(m.Name(), cfg, env):
			out = append(out, unsupportedModule{name: m.Name(), reason: reason})
		}
	}
	return out
}

func moduleConfigured(name string, cfg *config.Config, env Env) bool {
	switch name {
	case "system":
		return len(cfg.System.Dirs) > 0 || cfg.System.SudoTouchID || cfg.System.SSHKey != nil
	case "brew":
		return cfg.Brew != nil
	case "mise":
		return cfg.Mise != nil
	case "commands":
		return len(cfg.Commands) > 0
	case "defaults":
		return len(cfg.Defaults) > 0
	case "launchd":
		_, err := os.Lstat(launchd.StatePath(env.Home))
		return len(cfg.Launchd) > 0 || err == nil
	}
	return true
}
