package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

type commandHookRegistration struct {
	Event   string `json:"event"`
	Command string `json:"command"`
}

type commandHooksState struct {
	Providers map[string][]commandHookRegistration `json:"providers"`
	Pending   []commandHookRegistration            `json:"opencodePending,omitempty"`
}

// CommandHooksStatePath is the ownership record for command hooks.
func CommandHooksStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "command-hooks.json")
}

func (m *Module) commandHooksStatePath() string { return CommandHooksStatePath(m.Paths.Home) }

func (m *Module) loadCommandHooksState() (commandHooksState, error) {
	var st commandHooksState
	if err := loadState(m.commandHooksStatePath(), &st); err != nil {
		return st, err
	}
	if st.Providers == nil {
		st.Providers = map[string][]commandHookRegistration{}
	}
	return st, nil
}

func (m *Module) updateCommandHooksState(provider string, owned []commandHookRegistration) error {
	path := m.commandHooksStatePath()
	if err := install.CheckParents(m.Paths, path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", m.Paths.Pretty(path))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	st, err := m.loadCommandHooksState()
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		delete(st.Providers, provider)
	} else {
		st.Providers[provider] = owned
	}
	if provider == config.ProviderOpencode {
		st.Pending = nil
	}
	if len(st.Providers) == 0 && len(st.Pending) == 0 {
		if err := os.Remove(m.commandHooksStatePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveState(m.commandHooksStatePath(), st)
}

func (m *Module) stageOpencodeCommandHooks(current, want []commandHookRegistration) error {
	path := m.commandHooksStatePath()
	if err := install.CheckParents(m.Paths, path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", m.Paths.Pretty(path))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	st, err := m.loadCommandHooksState()
	if err != nil {
		return err
	}
	if len(current) == 0 {
		delete(st.Providers, config.ProviderOpencode)
	} else {
		st.Providers[config.ProviderOpencode] = current
	}
	st.Pending = want
	return saveState(m.commandHooksStatePath(), st)
}

func combinedRegistrations(old, next []commandHookRegistration) []commandHookRegistration {
	combined := slices.Clone(old)
	for _, entry := range next {
		if !slices.Contains(combined, entry) {
			combined = append(combined, entry)
		}
	}
	return combined
}

func (m *Module) desiredCommandHooks(provider string) []commandHookRegistration {
	if m.Agents.Hooks == nil {
		return nil
	}
	var result []commandHookRegistration
	for _, hook := range m.Agents.Hooks.Commands {
		providers := hook.Providers
		if providers == nil {
			providers = m.Agents.DefaultProviders()
		}
		if !slices.Contains(providers, provider) ||
			(provider == config.ProviderCodex && !m.desired(provider).named && hook.Providers == nil) {
			continue
		}
		result = append(result, commandHookRegistration{Event: hook.Event, Command: hook.Command})
	}
	return result
}

func (m *Module) planCommandHooks() (map[string][]engine.Change, map[string]error, error) {
	statePath := m.commandHooksStatePath()
	if m.Agents.Hooks == nil || len(m.Agents.Hooks.Commands) == 0 {
		if _, err := os.Lstat(statePath); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("agents.hooks.commands: %s is a symlink", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, nil, fmt.Errorf("agents.hooks.commands: %w", err)
	}
	st, err := m.loadCommandHooksState()
	if err != nil {
		return nil, nil, err
	}
	changes := map[string][]engine.Change{}
	failed := map[string]error{}
	for _, provider := range config.Providers {
		want := m.desiredCommandHooks(provider)
		if len(want) == 0 && len(st.Providers[provider]) == 0 &&
			(provider != config.ProviderOpencode || len(st.Pending) == 0) {
			continue
		}
		var c *engine.Change
		if provider == config.ProviderOpencode {
			c, err = m.planOpencodeCommandHooks(want)
		} else {
			c, err = m.planJSONCommandHooks(provider, want, st.Providers[provider])
		}
		if err != nil {
			failed[provider] = fmt.Errorf("%s: command hooks: %w", provider, err)
			continue
		}
		if c != nil {
			c.Group = provider
			changes[provider] = append(changes[provider], *c)
		}
	}
	return changes, failed, nil
}

func (m *Module) commandHooksPath(provider string) string {
	if provider == config.ProviderClaude {
		return filepath.Join(m.claudeBackend().dir, "settings.json")
	}
	return filepath.Join(m.codexBackend().home, "hooks.json")
}

func nativeHookEvent(provider, event string) string {
	if provider == config.ProviderClaude || provider == config.ProviderCodex {
		switch event {
		case "beforeTool":
			return "PreToolUse"
		case "afterTool":
			return "PostToolUse"
		case "sessionStart":
			return "SessionStart"
		case "stop":
			return "Stop"
		}
	}
	return ""
}

func commandHookGroup(command string) (json.RawMessage, error) {
	return marshal(struct {
		Hooks []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	}{Hooks: []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}{{Type: "command", Command: command}}})
}

func sameHookGroup(a, b json.RawMessage) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func hasCommandHookGroup(raw json.RawMessage, command string) bool {
	var group struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Async   bool   `json:"async"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &group) != nil || (group.Matcher != "" && group.Matcher != "*") {
		return false
	}
	for _, hook := range group.Hooks {
		if hook.Type == "command" && hook.Command == command && !hook.Async {
			return true
		}
	}
	return false
}

func (m *Module) renderJSONCommandHooks(provider string, want, owned []commandHookRegistration) (object, bool, []commandHookRegistration, bool, error) {
	path := m.commandHooksPath(provider)
	o, exists, err := readObject(path)
	if err != nil {
		return o, false, nil, false, err
	}
	var events map[string][]json.RawMessage
	if raw, ok := o.get("hooks"); ok {
		if err := json.Unmarshal(raw, &events); err != nil {
			return o, exists, nil, false, fmt.Errorf("%s hooks: %w", path, err)
		}
	}
	if events == nil {
		events = map[string][]json.RawMessage{}
	}
	before := map[string]any{}
	if data, err := marshal(o); err != nil {
		return o, exists, nil, false, err
	} else if err := json.Unmarshal(data, &before); err != nil {
		return o, exists, nil, false, err
	}
	for _, old := range owned {
		event := nativeHookEvent(provider, old.Event)
		group, err := commandHookGroup(old.Command)
		if err != nil {
			return o, exists, nil, false, err
		}
		for i, raw := range events[event] {
			if sameHookGroup(raw, group) {
				events[event] = append(events[event][:i], events[event][i+1:]...)
				break
			}
		}
		if len(events[event]) == 0 {
			delete(events, event)
		}
	}
	var nextOwned []commandHookRegistration
	for _, h := range want {
		event := nativeHookEvent(provider, h.Event)
		group, err := commandHookGroup(h.Command)
		if err != nil {
			return o, exists, nil, false, err
		}
		found := false
		for _, raw := range events[event] {
			if hasCommandHookGroup(raw, h.Command) {
				found = true
				break
			}
		}
		if !found {
			events[event] = append(events[event], group)
			nextOwned = append(nextOwned, h)
		} else if slices.Contains(owned, h) {
			nextOwned = append(nextOwned, h)
		}
	}
	if len(events) == 0 {
		o.delete("hooks")
	} else if err := o.set("hooks", events); err != nil {
		return o, exists, nil, false, err
	}
	data, err := marshal(o)
	if err != nil {
		return o, exists, nil, false, err
	}
	var after map[string]any
	if err := json.Unmarshal(data, &after); err != nil {
		return o, exists, nil, false, err
	}
	return o, exists, nextOwned, !reflect.DeepEqual(before, after), nil
}

func (m *Module) planJSONCommandHooks(provider string, want, owned []commandHookRegistration) (*engine.Change, error) {
	path := m.commandHooksPath(provider)
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", m.Paths.Pretty(path))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, exists, nextOwned, changed, err := m.renderJSONCommandHooks(provider, want, owned)
	if err != nil {
		return nil, err
	}
	if !changed && slices.Equal(owned, nextOwned) {
		return nil, nil
	}
	if changed && exists {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm()&0o200 == 0 {
			return nil, fmt.Errorf("%s is read-only; change it in the repo and apply it first", m.Paths.Pretty(path))
		}
	}
	action := engine.Update
	if !exists && changed {
		action = engine.Create
	}
	return &engine.Change{
		Action: action, Target: m.Paths.Pretty(path), Detail: "register shared command hooks for " + provider,
		Apply: func(context.Context) error {
			if err := install.CheckParents(m.Paths, path); err != nil {
				return err
			}
			if info, err := os.Stat(path); err == nil {
				if !info.Mode().IsRegular() || info.Mode().Perm()&0o200 == 0 {
					return fmt.Errorf("%s is not a writable regular file", m.Paths.Pretty(path))
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			st, err := m.loadCommandHooksState()
			if err != nil {
				return err
			}
			o, exists, nextOwned, changed, err := m.renderJSONCommandHooks(provider, want, st.Providers[provider])
			if err != nil {
				return err
			}
			if !changed {
				return m.updateCommandHooksState(provider, nextOwned)
			}
			if err := m.updateCommandHooksState(provider, combinedRegistrations(st.Providers[provider], nextOwned)); err != nil {
				return err
			}
			if !exists {
				data, err := marshal(o)
				if err != nil {
					return err
				}
				if err := install.WriteAtomic(path, append(data, '\n'), 0o600); err != nil {
					return err
				}
			} else if err := writeObject(path, o); err != nil {
				return err
			}
			return m.updateCommandHooksState(provider, nextOwned)
		},
	}, nil
}

func (m *Module) opencodeCommandHooksPath() string {
	base := filepath.Join(m.Paths.Home, ".config")
	if m.Paths.Home == os.Getenv("HOME") {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
			base = xdg
		}
	}
	return filepath.Join(base, "opencode", "plugins", "zakwas-hooks.js")
}

func renderOpencodeCommandHooks(hooks []commandHookRegistration) ([]byte, error) {
	byEvent := map[string][]string{}
	for _, hook := range hooks {
		byEvent[hook.Event] = append(byEvent[hook.Event], hook.Command)
	}
	commands, err := marshal(byEvent)
	if err != nil {
		return nil, err
	}
	return []byte(`// Generated by zakwas. Edit agents.hooks.commands in zakwas.yaml.
const commands = ` + string(commands) + `;

async function run(command, event) {
  const proc = Bun.spawn(["/bin/sh", "-c", command], {
    stdin: new Blob([JSON.stringify(event)]),
    stdout: "pipe",
    stderr: "pipe",
  });
  const [code, output, error] = await Promise.all([
    proc.exited,
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  if (code !== 0) throw new Error(error.trim() || ` + "`hook exited ${code}`" + `);
  try { return JSON.parse(output); } catch { return {}; }
}

async function runAll(eventName, event) {
  for (const command of commands[eventName] || []) {
    const result = await run(command, event);
    if (eventName === "beforeTool" && (result?.decision === "block" || result?.hookSpecificOutput?.permissionDecision === "deny")) {
      throw new Error(result.reason || result.hookSpecificOutput?.permissionDecisionReason || "hook blocked tool");
    }
  }
}

export default {
  id: "zakwas-hooks",
  async server() {
    return {
      ...(commands.beforeTool && {
        "tool.execute.before": (input, output) => runAll("beforeTool", { input, output }),
      }),
      ...(commands.afterTool && {
        "tool.execute.after": (input, output) => runAll("afterTool", { input, output }),
      }),
      ...((commands.sessionStart || commands.stop) && {
        event: async ({ event }) => {
          if (event.type === "session.created") await runAll("sessionStart", event);
          if (event.type === "session.idle") await runAll("stop", event);
        },
      }),
    };
  },
  async setup(ctx) {
    const registrations = [];
    if (commands.beforeTool) registrations.push(await ctx.tool.hook("execute.before", event => runAll("beforeTool", event)));
    if (commands.afterTool) registrations.push(await ctx.tool.hook("execute.after", event => runAll("afterTool", event)));
    let controller;
    if (commands.sessionStart || commands.stop) {
      controller = new AbortController();
      void (async () => {
        try {
          for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
            try {
              if (event.type === "session.created") await runAll("sessionStart", event);
              if (event.type === "session.execution.succeeded") await runAll("stop", event);
            } catch (error) { console.error(error); }
          }
        } catch (error) {
          if (!controller.signal.aborted) console.error(error);
        }
      })();
    }
    return () => {
      controller?.abort();
      for (const registration of registrations) registration.dispose();
    };
  },
};
`), nil
}

func (m *Module) planOpencodeCommandHooks(want []commandHookRegistration) (*engine.Change, error) {
	path := m.opencodeCommandHooksPath()
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	st, err := m.loadCommandHooksState()
	if err != nil {
		return nil, err
	}
	owned := st.Providers[config.ProviderOpencode]
	pending := st.Pending
	if len(want) > 0 && !m.Runner.Installed(config.ProviderOpencode) {
		if m.desired(config.ProviderOpencode).named {
			return nil, errors.New("opencode is not installed")
		}
		return nil, nil
	}
	var have []byte
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", m.Paths.Pretty(path))
	default:
		have, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	oldContent, err := renderOpencodeCommandHooks(owned)
	if err != nil {
		return nil, err
	}
	pendingContent, err := renderOpencodeCommandHooks(pending)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		if len(owned) == 0 && len(pending) == 0 {
			return nil, nil
		}
		detail := "remove shared command hooks"
		remove := have != nil && ((len(owned) > 0 && bytes.Equal(have, oldContent)) || (len(pending) > 0 && bytes.Equal(have, pendingContent)))
		if !remove {
			detail = "modified plugin, forget ownership"
		}
		return &engine.Change{
			Action: engine.Remove, Target: m.Paths.Pretty(path), Detail: detail, Destructive: remove,
			Apply: func(context.Context) error {
				if err := install.CheckParents(m.Paths, path); err != nil {
					return err
				}
				info, err := os.Lstat(path)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				current, err := os.ReadFile(path)
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				if info != nil && info.Mode().IsRegular() && ((len(owned) > 0 && bytes.Equal(current, oldContent)) || (len(pending) > 0 && bytes.Equal(current, pendingContent))) {
					if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
						return err
					}
				}
				return m.updateCommandHooksState(config.ProviderOpencode, nil)
			},
		}, nil
	}
	content, err := renderOpencodeCommandHooks(want)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(have, content) {
		if len(pending) > 0 || (len(owned) > 0 && !slices.Equal(owned, want)) {
			return &engine.Change{
				Action: engine.Update, Target: m.Paths.Pretty(path), Detail: "track shared command hooks",
				Apply: func(context.Context) error { return m.updateCommandHooksState(config.ProviderOpencode, want) },
			}, nil
		}
		return nil, nil
	}
	action := engine.Update
	if have == nil {
		action = engine.Create
	}
	return &engine.Change{
		Action: action, Target: m.Paths.Pretty(path), Detail: "register shared command hooks for opencode",
		Apply: func(context.Context) error {
			if err := install.CheckParents(m.Paths, path); err != nil {
				return err
			}
			exists := false
			if info, err := os.Lstat(path); err == nil {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("%s is not a regular file", m.Paths.Pretty(path))
				}
				exists = true
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			current, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			currentOwned := []commandHookRegistration(nil)
			switch {
			case exists && len(owned) > 0 && bytes.Equal(current, oldContent):
				currentOwned = owned
			case exists && len(pending) > 0 && bytes.Equal(current, pendingContent):
				currentOwned = pending
			case exists && !bytes.Equal(current, content):
				if _, err := install.Backup(path); err != nil {
					return err
				}
			}
			if err := m.stageOpencodeCommandHooks(currentOwned, want); err != nil {
				return err
			}
			if !bytes.Equal(current, content) {
				if err := install.WriteAtomic(path, content, 0o644); err != nil {
					return err
				}
			}
			return m.updateCommandHooksState(config.ProviderOpencode, want)
		},
	}, nil
}
