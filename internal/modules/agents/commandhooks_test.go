package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func TestCommandHooksConvergeAndRemove(t *testing.T) {
	home := t.TempDir()
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeHookTestFile(t, claudePath, `{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"personal-hook"}]}]}}`)
	m := &Module{
		Agents: config.Agents{
			Providers: []string{"claude", "codex", "opencode"},
			Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{
				{Event: "beforeTool", Command: "/bin/check-tool"},
				{Event: "stop", Command: "/bin/record-stop"},
			}},
		},
		Paths: config.Paths{Home: home}, Runner: runnertest.New(),
	}
	applyAgentChanges := func() {
		t.Helper()
		changes, err := m.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(changes) != 3 {
			t.Fatalf("changes = %d, want 3: %v", len(changes), changes)
		}
		for _, change := range changes {
			if err := change.Apply(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
	applyAgentChanges()
	for _, path := range []string{claudePath, filepath.Join(home, ".codex", "hooks.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var settings struct {
			Hooks map[string][]struct {
				Hooks []struct{ Command string } `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		if len(settings.Hooks["PreToolUse"]) != 1 || settings.Hooks["PreToolUse"][0].Hooks[0].Command != "/bin/check-tool" {
			t.Fatalf("before tool hooks in %s: %s", path, data)
		}
		if len(settings.Hooks["Stop"]) == 0 {
			t.Fatalf("missing stop hook in %s", path)
		}
	}
	pluginPath := m.opencodeCommandHooksPath()
	plugin, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plugin), `ctx.tool.hook("execute.before"`) ||
		!strings.Contains(string(plugin), `"tool.execute.before"`) ||
		!strings.Contains(string(plugin), `session.execution.succeeded`) ||
		!strings.Contains(string(plugin), `session.idle`) {
		t.Fatalf("OpenCode plugin missing registrations: %s", plugin)
	}
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("second plan = %v, %v", changes, err)
	}
	m.Agents.Hooks = nil
	applyAgentChanges()
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("removal plan = %v, %v", changes, err)
	}
	if _, err := os.Stat(pluginPath); !os.IsNotExist(err) {
		t.Fatalf("OpenCode plugin still exists: %v", err)
	}
	if _, err := os.Stat(m.commandHooksStatePath()); !os.IsNotExist(err) {
		t.Fatalf("command hooks state still exists: %v", err)
	}
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `personal-hook`) || strings.Contains(string(data), `/bin/check-tool`) || !strings.Contains(string(data), `"theme": "dark"`) {
		t.Fatalf("Claude unrelated settings changed: %s", data)
	}
}

func TestCommandHooksLeaveManualRegistration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeHookTestFile(t, path, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/bin/check-tool","timeout":30}]}]}}`)
	m := &Module{
		Agents: config.Agents{Providers: []string{"claude"}, Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "beforeTool", Command: "/bin/check-tool"}}}},
		Paths:  config.Paths{Home: home}, Runner: runnertest.New(),
	}
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("manual hook plan = %v, %v", changes, err)
	}
	m.Agents.Hooks = nil
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("manual hook removal plan = %v, %v", changes, err)
	}
}

func TestOpencodeCommandHookBridge(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not installed")
	}
	if err := exec.Command(bun, "--version").Run(); err != nil {
		t.Skip("bun is not configured")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "event.json")
	content, err := renderOpencodeCommandHooks([]commandHookRegistration{{Event: "beforeTool", Command: "/bin/cat > " + output}})
	if err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(dir, "zakwas-hooks.mjs")
	writeHookTestFile(t, plugin, string(content))
	script := `import plugin from "./zakwas-hooks.mjs";
const path = ` + string(mustJSON(t, output)) + `;
const v1 = await plugin.server();
await v1["tool.execute.before"]({ tool: "bash" }, { args: {} });
const first = JSON.parse(await Bun.file(path).text());
if (first.input.tool !== "bash") throw new Error("V1 hook did not receive input");
const callbacks = {};
await plugin.setup({ tool: { hook: async (name, callback) => { callbacks[name] = callback; } } });
await callbacks["execute.before"]({ tool: "read" });
const second = JSON.parse(await Bun.file(path).text());
if (second.tool !== "read") throw new Error("V2 hook did not receive input");
`
	scriptPath := filepath.Join(dir, "check.mjs")
	writeHookTestFile(t, scriptPath, script)
	cmd := exec.Command(bun, "run", scriptPath)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated OpenCode plugin failed: %v\n%s", err, output)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
