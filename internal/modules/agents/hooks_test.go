package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

type installingHooksRunner struct {
	*runnertest.Fake
	install func() error
}

func (r *installingHooksRunner) Run(ctx context.Context, cmd runner.Cmd) (runner.Result, error) {
	result, err := r.Fake.Run(ctx, cmd)
	if err == nil && result.ExitCode == 0 && cmd.Name == "klaudiush" {
		err = r.install()
	}
	return result, err
}

func TestKlaudiushHooksConverge(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "bin", "klaudiush")
	writeHookTestFile(t, binary, "klaudiush test binary")
	codexPath := filepath.Join(home, "custom", "codex-hooks.json")
	pluginPath := filepath.Join(home, "custom", "klaudiush.ts")
	writeHookTestFile(t, filepath.Join(home, ".config", "klaudiush", "config.toml"), "[providers.claude]\nenabled = true\n[providers.codex]\nenabled = true\nexperimental = true\nhooks_config_path = \""+codexPath+"\"\n[providers.opencode]\nenabled = true\nplugin_path = \""+pluginPath+"\"\n")
	writeHookTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PreToolUse"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PostToolUse"}]}]}}`)
	fullCodex := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + binary + ` --provider codex --event SessionStart"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"` + binary + ` --provider codex --event PreToolUse"}]}],"Stop":[{"hooks":[{"type":"command","command":"` + binary + ` --provider codex --event Stop"}]}]}}`
	writeHookTestFile(t, codexPath, fullCodex)
	writeHookTestFile(t, pluginPath, `const BINARY = "`+binary+`"; ctx.tool.hook("execute.before", () => {}); case "session.execution.succeeded":`)
	installer := &installingHooksRunner{Fake: runnertest.New().OnOK("klaudiush init --install-hooks --global", ""), install: func() error { return nil }}
	m := &Module{Agents: config.Agents{Hooks: &config.AgentHooks{Klaudiush: true}}, Paths: config.Paths{Home: home}, Runner: installer, klaudiushPath: binary}
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("initial plan = %v, %v", changes, err)
	}
	if err := changes[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("second plan = %v, %v", changes, err)
	}
	writeHookTestFile(t, codexPath, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"`+binary+` --provider codex --event SessionStart"}]}]}}`)
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Detail, "codex") {
		t.Fatalf("drift plan = %v, %v", changes, err)
	}
	installer.install = func() error { return os.WriteFile(codexPath, []byte(fullCodex), 0o644) }
	if err := changes[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("third plan = %v, %v", changes, err)
	}
	writeHookTestFile(t, binary, "upgraded klaudiush binary")
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Detail, "refresh") {
		t.Fatalf("binary upgrade plan = %v, %v", changes, err)
	}
	if err := changes[0].Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(home, ".claude", "settings.json")
	file, err := os.OpenFile(claudePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(context.Background())
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Detail, "refresh") {
		t.Fatalf("hook file drift plan = %v, %v", changes, err)
	}
}

func TestKlaudiushHooksMissingConfigPlansInstall(t *testing.T) {
	home := t.TempDir()
	m := &Module{Agents: config.Agents{Hooks: &config.AgentHooks{Klaudiush: true}}, Paths: config.Paths{Home: home}, Runner: runnertest.New()}
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Detail, "config pending") {
		t.Fatalf("missing config plan = %v, %v", changes, err)
	}
}

func TestKlaudiushHooksMalformedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeHookTestFile(t, path, `{broken`)
	_, err := hooksRegistered(path, []string{"PreToolUse"}, func(string) string { return "klaudiush" })
	if err == nil {
		t.Fatal("malformed settings must fail planning")
	}
}

func TestKlaudiushCodexLegacyHookDoesNotConverge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	writeHookTestFile(t, path, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"klaudiush --provider codex --event SessionStart"}]}],"AfterToolUse":[{"hooks":[{"type":"command","command":"klaudiush --provider codex --event AfterToolUse"}]}],"Stop":[{"hooks":[{"type":"command","command":"klaudiush --provider codex --event Stop"}]}]}}`)
	ok, err := hooksRegistered(path, []string{"SessionStart", "PreToolUse", "Stop"}, func(event string) string {
		return "klaudiush --provider codex --event " + event
	})
	if err != nil || ok {
		t.Fatalf("legacy Codex hooks registered = %v, %v", ok, err)
	}
}

func TestKlaudiushOpencodeHookVersions(t *testing.T) {
	binary := "/usr/local/bin/klaudiush"
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"v1", `const BINARY = "` + binary + `"; "tool.execute.before": async () => {}; case "session.idle":`, true},
		{"v2", `const BINARY = "` + binary + `"; ctx.tool.hook("execute.before", () => {}); case "session.execution.succeeded":`, true},
		{"missing before", `const BINARY = "` + binary + `"; case "session.execution.succeeded":`, false},
		{"missing idle", `const BINARY = "` + binary + `"; ctx.tool.hook("execute.before", () => {});`, false},
		{"mixed versions", `const BINARY = "` + binary + `"; "tool.execute.before": async () => {}; case "session.execution.succeeded":`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "klaudiush.ts")
			writeHookTestFile(t, path, tt.source)
			got, err := opencodeHookRegistered(path, binary)
			if err != nil || got != tt.want {
				t.Fatalf("OpenCode hooks registered = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func writeHookTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
