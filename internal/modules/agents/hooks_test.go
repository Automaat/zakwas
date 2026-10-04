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
	writeHookTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PreToolUse"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PostToolUse"}]}],"PostToolUseFailure":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PostToolUseFailure"}]}]}}`)
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

func TestKlaudiushFingerprintAfterSharedHookWrite(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		name := "initial registration"
		if initialized {
			name = "current registration"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			binary := filepath.Join(home, "bin", "klaudiush")
			writeHookTestFile(t, binary, "klaudiush binary")
			writeHookTestFile(t, filepath.Join(home, ".config", "klaudiush", "config.toml"), "[providers.claude]\nenabled = true\n")
			settings := filepath.Join(home, ".claude", "settings.json")
			writeHookTestFile(t, settings, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PreToolUse"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PostToolUse"}]}],"PostToolUseFailure":[{"hooks":[{"type":"command","command":"`+binary+` --hook-type PostToolUseFailure"}]}]}}`)
			m := &Module{
				Agents: config.Agents{Providers: []string{"claude"}, Hooks: &config.AgentHooks{Klaudiush: true, Commands: []config.AgentCommandHook{{Event: "beforeTool", Command: "/bin/check"}}}},
				Paths:  config.Paths{Home: home}, Runner: runnertest.New().OnOK("klaudiush init --install-hooks --global", ""), klaudiushPath: binary,
			}
			if initialized {
				state, err := m.captureKlaudiushState()
				if err != nil {
					t.Fatal(err)
				}
				if err := saveState(m.klaudiushStatePath(), state); err != nil {
					t.Fatal(err)
				}
			}
			changes, err := m.Plan(context.Background())
			if err != nil || len(changes) != 2 || changes[1].Group != "klaudiush" {
				t.Fatalf("initial plan = %v, %v", changes, err)
			}
			for _, change := range changes {
				if err := change.Apply(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			changes, err = m.Plan(context.Background())
			if err != nil || len(changes) != 0 {
				t.Fatalf("second plan = %v, %v", changes, err)
			}
		})
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

func TestKlaudiushCodexPreToolEnforcement(t *testing.T) {
	for _, tt := range []struct {
		name, matcher, async string
		want                 bool
	}{
		{"all tools", "", "false", true},
		{"wildcard", " * ", "false", true},
		{"specific tool", "Bash", "false", false},
		{"asynchronous", "", "true", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			writeHookTestFile(t, path, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"klaudiush --provider codex --event SessionStart"}]}],"Stop":[{"hooks":[{"type":"command","command":"klaudiush --provider codex --event Stop"}]}],"PreToolUse":[{"matcher":"`+tt.matcher+`","hooks":[{"type":"command","command":"klaudiush --provider codex --event PreToolUse","async":`+tt.async+`}]}]}}`)
			got, err := codexHooksRegistered(path, "klaudiush")
			if err != nil || got != tt.want {
				t.Fatalf("Codex hooks registered = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestKlaudiushLegacyConfigPath(t *testing.T) {
	home := t.TempDir()
	m := &Module{Paths: config.Paths{Home: home}}
	legacy := filepath.Join(home, ".klaudiush", "config.toml")
	current := filepath.Join(home, ".config", "klaudiush", "config.toml")
	writeHookTestFile(t, legacy, "[providers.claude]\n")
	if got := m.klaudiushConfigPath(); got != legacy {
		t.Fatalf("config path = %s, want %s", got, legacy)
	}
	writeHookTestFile(t, current, "[providers.claude]\n")
	if got := m.klaudiushConfigPath(); got != current {
		t.Fatalf("config path = %s, want %s", got, current)
	}
}

func TestKlaudiushGeminiEvidenceEvents(t *testing.T) {
	for _, tt := range []struct {
		name, filter string
		want         bool
	}{
		{"default filter", "", true},
		{"explicit filter", "filter_tools = true\n", true},
		{"disabled filter", "filter_tools = false\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			binary := filepath.Join(home, "bin", "klaudiush")
			settings := filepath.Join(home, "gemini.json")
			writeHookTestFile(t, binary, "binary")
			writeHookTestFile(t, filepath.Join(home, ".config", "klaudiush", "config.toml"), "[providers.claude]\nenabled = false\n[providers.gemini]\nenabled = true\nsettings_path = \""+settings+"\"\n[evidence.tool_phase]\nenabled = true\n"+tt.filter)
			events := []string{"BeforeTool", "AfterTool", "AfterAgent", "SessionStart", "SessionEnd", "Notification", "PreCompress"}
			var groups []string
			for _, event := range events {
				groups = append(groups, `"`+event+`": [{"hooks":[{"type":"command","command":"`+binary+` --provider gemini --event `+event+`"}]}]`)
			}
			writeHookTestFile(t, settings, `{"hooks":{`+strings.Join(groups, ",")+`}}`)
			m := &Module{Agents: config.Agents{Hooks: &config.AgentHooks{Klaudiush: true}}, Paths: config.Paths{Home: home}, Runner: runnertest.New(), klaudiushPath: binary}
			change, err := m.planKlaudiush()
			if err != nil {
				t.Fatal(err)
			}
			missing := change != nil && strings.Contains(change.Detail, "gemini")
			if missing != tt.want {
				t.Fatalf("missing Gemini registration = %v, want %v; change = %v", missing, tt.want, change)
			}
		})
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
