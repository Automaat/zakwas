package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

// HookFilesStatePath is where zakwas records the hash of the hooks file it
// wrote for Copilot CLI under home.
func HookFilesStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "agent-hook-files.json")
}

func (m *Module) hookFilesStatePath() string { return HookFilesStatePath(m.Paths.Home) }

// hookInstaller writes the hooks file, with a state of its own apart from
// the files module's, which would remove it as an orphan.
func (m *Module) hookInstaller() *install.Installer {
	if m.hooks == nil {
		m.hooks = &install.Installer{Paths: m.Paths, StatePath: m.hookFilesStatePath(), Immutable: m.Immutable}
	}
	return m.hooks
}

// copilotHooksPath is a file of its own in Copilot's hooks directory, which
// it reads every *.json from, so zakwas never edits a file it doesn't own.
func (m *Module) copilotHooksPath() string {
	return filepath.Join(m.copilotBackend().home, "hooks", "zakwas.json")
}

// copilotHookEvents are Copilot's names for the shared events.
var copilotHookEvents = map[string]string{
	"beforeTool": "preToolUse", "afterTool": "postToolUse", "sessionStart": "sessionStart", "stop": "agentStop",
}

type copilotHook struct {
	Type string `json:"type"`
	Bash string `json:"bash"`
}

type copilotHooksFile struct {
	Version int                      `json:"version"`
	Hooks   map[string][]copilotHook `json:"hooks"`
}

// renderCopilotHooks is the hooks file for want, in the order declared.
func renderCopilotHooks(want []commandHookRegistration) ([]byte, error) {
	f := copilotHooksFile{Version: 1, Hooks: map[string][]copilotHook{}}
	for _, h := range want {
		event := copilotHookEvents[h.Event]
		f.Hooks[event] = append(f.Hooks[event], copilotHook{Type: "command", Bash: h.Command})
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// planCopilotCommandHooks writes agents.hooks.commands for Copilot to a
// hooks file of zakwas's own, and removes it when no hook wants it. Only
// a named copilot, or a hook that lists it, gets one.
func (m *Module) planCopilotCommandHooks() ([]engine.Change, error) {
	statePath := m.hookFilesStatePath()
	want := m.desiredCommandHooks(config.ProviderCopilot)
	if len(want) == 0 {
		if _, err := os.Lstat(statePath); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
	}
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; remove it", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, err
	}
	in := m.hookInstaller()
	tracked, err := in.Tracked()
	if err != nil {
		return nil, err
	}
	dst := m.copilotHooksPath()
	var changes []engine.Change
	if len(want) > 0 {
		data, err := renderCopilotHooks(want)
		if err != nil {
			return nil, err
		}
		c, err := in.Plan(dst, data, 0o644)
		if err != nil {
			return nil, err
		}
		if c != nil {
			if c.Detail == "" {
				c.Detail = "register shared command hooks for copilot"
			}
			c.Group = config.ProviderCopilot
			changes = append(changes, *c)
		}
	}
	for _, old := range tracked {
		if len(want) > 0 && samePath(old) == samePath(dst) {
			continue
		}
		c, err := m.planCopyRemove(in, statePath, old, "hooks no longer declared")
		if err != nil {
			return nil, err
		}
		c.Group = config.ProviderCopilot
		changes = append(changes, c)
	}
	return changes, nil
}
