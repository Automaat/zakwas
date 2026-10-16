package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func tildeServers() map[string]config.MCPServer {
	return map[string]config.MCPServer{
		"tool": {
			Command: "~/.local/bin/tool",
			Args:    []string{"--config", "~/cfg/tool.json", "--dir=~/keep", "~", "~other/x", "plain"},
			Env:     map[string]string{"DATA": "~/data", "LITERAL": "a~/b", "BARE": "~"},
			EnvVars: []string{"TOKEN"},
		},
	}
}

func tildeModule(t *testing.T, providers []string) *Module {
	t.Helper()
	home := filepath.Join(t.TempDir(), "Zażółć me")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Module{
		Agents: config.Agents{Providers: providers, MCP: tildeServers()},
		Paths:  config.Paths{Home: home, Root: t.TempDir()},
		Runner: runnertest.New(),
	}
	m.CodexHome = filepath.Join(home, "codex")
	m.CopilotHome = filepath.Join(home, "copilot")
	m.ClaudeConfigDir = filepath.Join(home, "claude")
	return m
}

func TestMCPExpandsTildeClaude(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude})
	home := m.Paths.Home
	changes := plan(t, m)
	var diff strings.Builder
	for _, c := range changes {
		diff.WriteString(c.Diff)
	}
	if !strings.Contains(diff.String(), home+"/.local/bin/tool") || strings.Contains(diff.String(), `"~/.local`) {
		t.Errorf("plan diff does not show the expanded command:\n%s", diff.String())
	}
	apply(t, changes)

	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(m.ClaudeConfigDir, ".claude.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.MCPServers["tool"]
	if got.Command != home+"/.local/bin/tool" {
		t.Errorf("command = %q", got.Command)
	}
	wantArgs := []string{"--config", home + "/cfg/tool.json", "--dir=~/keep", home, "~other/x", "plain"}
	if !slices.Equal(got.Args, wantArgs) {
		t.Errorf("args = %q, want %q", got.Args, wantArgs)
	}
	wantEnv := map[string]string{"DATA": home + "/data", "LITERAL": "a~/b", "BARE": "~", "TOKEN": "${TOKEN}"}
	if len(got.Env) != len(wantEnv) {
		t.Errorf("env = %v, want %v", got.Env, wantEnv)
	}
	for k, v := range wantEnv {
		if got.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, got.Env[k], v)
		}
	}
	assertConverged(t, m)
	assertConverged(t, m)
}

func TestMCPExpandsTildeCodex(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderCodex})
	home := m.Paths.Home
	apply(t, plan(t, m))
	want := "[mcp_servers.tool]\n" +
		`command = "` + home + `/.local/bin/tool"` + "\n" +
		`args = ["--config", "` + home + `/cfg/tool.json", "--dir=~/keep", "` + home + `", "~other/x", "plain"]` + "\n" +
		`env_vars = ["TOKEN"]` + "\n" +
		"[mcp_servers.tool.env]\n" +
		`BARE = "~"` + "\n" +
		`DATA = "` + home + `/data"` + "\n" +
		`LITERAL = "a~/b"` + "\n"
	got := readFile(t, filepath.Join(m.CodexHome, "config.toml"))
	if normalizeSpace(got) != normalizeSpace(want) {
		t.Errorf("config.toml =\n%s\nwant\n%s", got, want)
	}
	assertConverged(t, m)
}

func normalizeSpace(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func TestMCPExpandsTildeCopilot(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderCopilot})
	home := m.Paths.Home
	apply(t, plan(t, m))
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(m.CopilotHome, "mcp-config.json"))), &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.MCPServers["tool"]
	if got.Command != home+"/.local/bin/tool" || got.Env["DATA"] != home+"/data" || got.Env["LITERAL"] != "a~/b" {
		t.Errorf("entry = %+v", got)
	}
	if want := []string{"--config", home + "/cfg/tool.json", "--dir=~/keep", home, "~other/x", "plain"}; !slices.Equal(got.Args, want) {
		t.Errorf("args = %q, want %q", got.Args, want)
	}
	assertConverged(t, m)
}

func TestMCPExpandsTildeGemini(t *testing.T) {
	m, _ := newGemini(t, config.Agents{MCP: tildeServers()})
	home := m.Paths.Home
	apply(t, plan(t, m))
	var manifest geminiManifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(home, ".gemini", "zakwas", "gemini-extension.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	got, ok := manifest.MCPServers["tool"]
	if !ok {
		t.Fatalf("tool was left out of Gemini: %v", m.geminiWarnings())
	}
	if got.Command != home+"/.local/bin/tool" || got.Env["DATA"] != home+"/data" || got.Env["BARE"] != "~" {
		t.Errorf("entry = %+v", got)
	}
	if want := []string{"--config", home + "/cfg/tool.json", "--dir=~/keep", home, "~other/x", "plain"}; !slices.Equal(got.Args, want) {
		t.Errorf("args = %q, want %q", got.Args, want)
	}
	assertConverged(t, m)
}

func TestMCPTildeIsNotADollarForGemini(t *testing.T) {
	m, _ := newGemini(t, config.Agents{MCP: tildeServers()})
	for _, w := range m.geminiWarnings() {
		if strings.Contains(w, "tool") {
			t.Errorf("warning for a tilde-only server: %s", w)
		}
	}
	m.Agents.MCP = map[string]config.MCPServer{"dollar": {Command: "$HOME/bin/x"}}
	if w := m.geminiWarnings(); len(w) != 1 || !strings.Contains(w[0], "dollar") {
		t.Errorf("$HOME is not expanded and must still be left out of Gemini: %q", w)
	}
}

func TestMCPAdoptsAbsoluteEntryForTildeDeclaration(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude, config.ProviderCodex, config.ProviderCopilot})
	home := m.Paths.Home
	m.Agents.MCP = map[string]config.MCPServer{"tool": {Command: "~/.local/bin/tool", Args: []string{"~/cfg"}}}
	claude := filepath.Join(m.ClaudeConfigDir, ".claude.json")
	copilot := filepath.Join(m.CopilotHome, "mcp-config.json")
	codex := filepath.Join(m.CodexHome, "config.toml")
	writeFile(t, claude, `{"mcpServers": {"tool": {"command": "`+home+`/.local/bin/tool", "args": ["`+home+`/cfg"]}}}`)
	writeFile(t, copilot, `{"mcpServers": {"tool": {"tools": ["*"], "type": "local", "command": "`+home+`/.local/bin/tool", "args": ["`+home+`/cfg"]}}}`)
	writeFile(t, codex, "[mcp_servers.tool]\ncommand = '"+home+"/.local/bin/tool'\nargs = ['"+home+"/cfg']\n")
	before := []string{readFile(t, claude), readFile(t, copilot), readFile(t, codex)}

	changes := plan(t, m)
	want := []string{
		"~ claude mcp tool (adopt the server already there)",
		"~ codex mcp tool (adopt the server already there)",
		"~ copilot mcp tool (adopt the server already there)",
	}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	after := []string{readFile(t, claude), readFile(t, copilot), readFile(t, codex)}
	if !slices.Equal(before, after) {
		t.Error("adopting rewrote a config file")
	}
	assertConverged(t, m)
}

func TestMCPAbsoluteDeclarationAdoptsSameEntry(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude})
	home := m.Paths.Home
	m.Agents.MCP = map[string]config.MCPServer{"tool": {Command: home + "/.local/bin/tool"}}
	cfg := filepath.Join(m.ClaudeConfigDir, ".claude.json")
	writeFile(t, cfg, `{"mcpServers": {"tool": {"command": "`+home+`/.local/bin/tool"}}}`)
	if got, want := targets(plan(t, m)), []string{"~ claude mcp tool (adopt the server already there)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}

func TestMCPLiteralTildeEntryDiffersFromDeclaration(t *testing.T) {
	for name, declared := range map[string]string{"tilde": "~/.local/bin/tool", "absolute": "/opt/tool"} {
		t.Run(name, func(t *testing.T) {
			m := tildeModule(t, []string{config.ProviderClaude})
			m.Agents.MCP = map[string]config.MCPServer{"tool": {Command: declared}}
			writeFile(t, filepath.Join(m.ClaudeConfigDir, ".claude.json"), `{"mcpServers": {"tool": {"command": "~/.local/bin/tool"}}}`)
			_, err := planErr(t, m)
			if err == nil || !strings.Contains(err.Error(), `server "tool"`) || !strings.Contains(err.Error(), "differs from zakwas.yaml in command") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), m.Paths.Home+"/.local/bin/tool") {
				t.Errorf("error repeats a value: %v", err)
			}
		})
	}
}

func TestMCPTildeUpdateAndRemoval(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude})
	apply(t, plan(t, m))

	s := m.Agents.MCP["tool"]
	s.Command = "~/.local/bin/tool2"
	m.Agents.MCP["tool"] = s
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.HasPrefix(got[0], "~ claude mcp tool") {
		t.Fatalf("plan = %q", got)
	}
	apply(t, changes)
	assertConverged(t, m)

	m.Agents.MCP = nil
	changes = plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.HasPrefix(got[0], "- claude mcp tool") {
		t.Fatalf("removal plan = %q", got)
	}
	apply(t, changes)
	assertConverged(t, m)
}

func TestMCPDoesNotChangeTheConfig(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude})
	plan(t, m)
	if got := m.Agents.MCP["tool"].Command; got != "~/.local/bin/tool" {
		t.Errorf("plan rewrote the declared command to %q", got)
	}
}

func TestCaptureKeepsPathsAbsoluteAndRoundTrips(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude}})
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "abs": {"type": "stdio", "command": "/opt/tools/tool", "args": ["/opt/tools/cfg"]},
  "lit": {"type": "stdio", "command": "~/.local/bin/tool", "args": ["~/cfg"]}}}`)
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)

	abs := find(t, got, KindMCP, "abs")
	if abs.Skip != "" || abs.Server == nil || abs.Server.Command != "/opt/tools/tool" || !slices.Equal(abs.Server.Args, []string{"/opt/tools/cfg"}) {
		t.Errorf("abs = %+v", abs)
	}
	lit := find(t, got, KindMCP, "lit")
	if lit.Server != nil || !strings.Contains(lit.Skip, "literal ~") {
		t.Errorf("lit = %+v", lit)
	}

	m.Agents.MCP = map[string]config.MCPServer{"abs": abs.Server.mcp()}
	if got := targets(plan(t, m)); !slices.Equal(got, []string{"~ claude mcp abs (adopt the server already there)"}) {
		t.Errorf("declaring the captured server plans %q", got)
	}
}

func TestCaptureRoundTripUnderHome(t *testing.T) {
	store := claudeMCP{m: &Module{}}
	const home = "/Users/me"
	server := CapturedServer{Command: home + "/.local/bin/tool", Args: []string{home + "/cfg"}}
	have, err := store.render("tool", server.mcp())
	if err != nil {
		t.Fatal(err)
	}
	if reason := roundTrip(store, "tool", server, have, home); reason != "" {
		t.Errorf("an absolute path under home does not round trip: %s", reason)
	}
	literal := CapturedServer{Command: "~/.local/bin/tool"}
	have, err = store.render("tool", literal.mcp())
	if err != nil {
		t.Fatal(err)
	}
	if reason := roundTrip(store, "tool", literal, have, home); !strings.Contains(reason, "literal ~") {
		t.Errorf("reason = %q", reason)
	}
}

func TestCaptureTildeReasonOnlyWhenTheTildeIsTheOnlyGap(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude}})
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "lit": {"type": "stdio", "command": "~/.local/bin/tool"},
  "lit-env": {"type": "stdio", "command": "~/.local/bin/tool", "env": {"A": "b"}},
  "lit-extra": {"type": "stdio", "command": "~/.local/bin/tool", "timeout": 5}}}`)
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	if c := find(t, got, KindMCP, "lit"); !strings.Contains(c.Skip, "literal ~") {
		t.Errorf("lit = %q", c.Skip)
	}
	for name, key := range map[string]string{"lit-env": "env", "lit-extra": "timeout"} {
		c := find(t, got, KindMCP, name)
		if strings.Contains(c.Skip, "literal ~") || !strings.Contains(c.Skip, "("+key+")") {
			t.Errorf("%s = %q", name, c.Skip)
		}
	}
}

func TestMCPRelativeHomeFailsOnlyWhenATildeNeedsIt(t *testing.T) {
	for _, provider := range []string{config.ProviderClaude, config.ProviderCodex, config.ProviderCopilot} {
		t.Run(provider, func(t *testing.T) {
			m := tildeModule(t, []string{provider})
			m.Paths.Home = "rel2"
			_, err := planErr(t, m)
			if err == nil || !strings.Contains(err.Error(), "agents.mcp.tool") || !strings.Contains(err.Error(), "HOME must be an absolute path") {
				t.Fatalf("err = %v", err)
			}

			m.Agents.MCP = map[string]config.MCPServer{"plain": {Command: "npx", Args: []string{"-y", "--a=~/x"}}}
			if _, err := planErr(t, m); err != nil && strings.Contains(err.Error(), "HOME must be") {
				t.Errorf("a server without a leading ~ failed on the home: %v", err)
			}
		})
	}
}

func TestMCPRelativeHomeFailsGemini(t *testing.T) {
	m, _ := newGemini(t, config.Agents{MCP: tildeServers()})
	m.Paths.Home = "rel2"
	_, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), "agents.mcp.tool") || !strings.Contains(err.Error(), "HOME must be an absolute path") {
		t.Fatalf("err = %v", err)
	}
}

func TestMCPBareTildeAdoptsAServerRegisteredWithATrailingSlashHome(t *testing.T) {
	m := tildeModule(t, []string{config.ProviderClaude})
	home := m.Paths.Home
	m.Paths.Home = home + "/"
	m.Agents.MCP = map[string]config.MCPServer{"tool": {Command: "x", Args: []string{"~", "~/", "~/a"}}}
	writeFile(t, filepath.Join(m.ClaudeConfigDir, ".claude.json"), `{"mcpServers": {"tool": {"command": "x", "args": ["`+home+`", "`+home+`/", "`+home+`/a"]}}}`)
	if got, want := targets(plan(t, m)), []string{"~ claude mcp tool (adopt the server already there)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
}
