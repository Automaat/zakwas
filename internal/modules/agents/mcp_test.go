package agents

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func mcpModule(t *testing.T, providers []string, servers map[string]config.MCPServer) *Module {
	t.Helper()
	m := &Module{
		Agents: config.Agents{Providers: providers, MCP: servers},
		Paths:  config.Paths{Home: t.TempDir(), Root: t.TempDir()},
		Runner: runnertest.New(),
	}
	m.CodexHome = filepath.Join(m.Paths.Home, "codex")
	return m
}

func stdioServer(command string, args ...string) config.MCPServer {
	return config.MCPServer{Command: command, Args: args}
}

func planErr(t *testing.T, m *Module) ([]engine.Change, error) {
	t.Helper()
	return m.Plan(context.Background())
}

func TestClaudeMCPLifecycle(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{
		"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"LANG": "C"}, EnvVars: []string{"DOCS_TOKEN"}},
		"web":  {URL: "https://example.com/mcp?key=secret", BearerTokenEnv: "WEB_TOKEN", HeaderEnv: map[string]string{"X-Org": "ORG"}, Headers: map[string]string{"X-Static": "1"}},
	})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	writeFile(t, cfg, `{
  "numStartups": 3,
  "mcpServers": {
    "mine": {"type": "stdio", "command": "mine"}
  },
  "projects": {}
}`)

	changes := plan(t, m)
	want := []string{"+ claude mcp docs (stdio npx)", "+ claude mcp web (http https://example.com/mcp)"}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if len(m.Runner.(*runnertest.Fake).Calls) != 0 {
		t.Errorf("plan ran %v", m.Runner.(*runnertest.Fake).Lines())
	}
	apply(t, changes)
	want0 := `{
  "numStartups": 3,
  "mcpServers": {
    "mine": {
      "type": "stdio",
      "command": "mine"
    },
    "docs": {
      "type": "stdio",
      "command": "npx",
      "args": [
        "-y",
        "docs-mcp"
      ],
      "env": {
        "DOCS_TOKEN": "${DOCS_TOKEN}",
        "LANG": "C"
      }
    },
    "web": {
      "type": "http",
      "url": "https://example.com/mcp?key=secret",
      "headers": {
        "Authorization": "Bearer ${WEB_TOKEN}",
        "X-Org": "${ORG}",
        "X-Static": "1"
      }
    }
  },
  "projects": {}
}
`
	if got := readFile(t, cfg); got != want0 {
		t.Fatalf(".claude.json =\n%s\nwant\n%s", got, want0)
	}
	assertConverged(t, m)

	docs := m.Agents.MCP["docs"]
	docs.Args = []string{"-y", "docs-mcp@2"}
	m.Agents.MCP["docs"] = docs
	delete(m.Agents.MCP, "web")
	changes = plan(t, m)
	want = []string{"~ claude mcp docs (stdio npx)", "- claude mcp web (no longer in zakwas.yaml)"}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("update plan = %q, want %q", got, want)
	}
	if !changes[1].Destructive || !strings.Contains(changes[0].Diff, "+    \"docs-mcp@2\"") {
		t.Errorf("changes = %+v", changes)
	}
	apply(t, changes)
	got := readFile(t, cfg)
	if !strings.Contains(got, `"docs-mcp@2"`) || strings.Contains(got, "web") || !strings.Contains(got, `"mine"`) || !strings.Contains(got, `"numStartups": 3`) {
		t.Errorf(".claude.json after update =\n%s", got)
	}
	assertConverged(t, m)

	m.Agents = config.Agents{}
	changes = plan(t, m)
	if got, want := targets(changes), []string{"- claude mcp docs (no longer in zakwas.yaml)"}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got := readFile(t, cfg); strings.Contains(got, "docs") || !strings.Contains(got, `"mine"`) {
		t.Errorf(".claude.json after removal =\n%s", got)
	}
	if _, err := os.Stat(m.mcpStatePath()); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
	assertConverged(t, m)
}

func TestClaudeMCPCreatesAndDropsConfig(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	apply(t, plan(t, m))
	if got, want := readFile(t, cfg), "{\n  \"mcpServers\": {\n    \"a\": {\n      \"type\": \"stdio\",\n      \"command\": \"a\",\n      \"args\": []\n    }\n  }\n}\n"; got != want {
		t.Errorf(".claude.json = %q, want %q", got, want)
	}
	if info, err := os.Stat(cfg); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", info, err)
	}
	m.Agents.MCP = nil
	apply(t, plan(t, m))
	if got := readFile(t, cfg); got != "{}\n" {
		t.Errorf(".claude.json after removal = %q", got)
	}
	assertConverged(t, m)
}

func TestClaudeMCPFollowsConfigDir(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"a": stdioServer("a")})
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "alt")
	apply(t, plan(t, m))
	if !strings.Contains(readFile(t, filepath.Join(m.ClaudeConfigDir, ".claude.json")), `"a"`) {
		t.Fatal("server not written to $CLAUDE_CONFIG_DIR/.claude.json")
	}

	m.ClaudeConfigDir = ""
	changes := plan(t, m)
	if got, want := targets(changes), []string{"+ claude mcp a (stdio a)", "- claude mcp a (no longer in zakwas.yaml)"}; !slices.Equal(got, want) {
		t.Fatalf("move plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if strings.Contains(readFile(t, filepath.Join(m.Paths.Home, "alt", ".claude.json")), `"a"`) {
		t.Error("server left in the old config dir")
	}
	assertConverged(t, m)
}

func TestMCPAdoptsIdenticalServer(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{
		"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}, EnvVars: []string{"B", "A"}},
	})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	writeFile(t, cfg, `{"mcpServers": {"docs": {"command": "npx", "args": ["-y", "docs-mcp"], "env": {"A": "${A}", "B": "${B}"}}}}`)
	toml := "# mine\n[mcp_servers.docs]\ncommand = 'npx'\nargs = [\n  \"-y\", \"docs-mcp\",\n]\nenv_vars = [\"A\", \"B\"]\n"
	writeFile(t, filepath.Join(m.CodexHome, "config.toml"), toml)
	beforeClaude := readFile(t, cfg)

	changes := plan(t, m)
	want := []string{"~ claude mcp docs (adopt the server already there)", "~ codex mcp docs (adopt the server already there)"}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if readFile(t, cfg) != beforeClaude || readFile(t, filepath.Join(m.CodexHome, "config.toml")) != toml {
		t.Error("adopting rewrote a config file")
	}
	assertConverged(t, m)

	m.Agents.MCP = nil
	changes = plan(t, m)
	if got, want := targets(changes), []string{"- claude mcp docs (no longer in zakwas.yaml)", "- codex mcp docs (no longer in zakwas.yaml)"}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got := readFile(t, filepath.Join(m.CodexHome, "config.toml")); got != "# mine\n" {
		t.Errorf("config.toml after removal = %q", got)
	}
}

func TestMCPFailsOnDifferingServer(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{
		"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}},
	})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	claudeBefore := `{"mcpServers": {"docs": {"type": "stdio", "command": "other", "env": {"SECRET": "hunter2"}}}}`
	writeFile(t, cfg, claudeBefore)
	codexConfig := filepath.Join(m.CodexHome, "config.toml")
	writeFile(t, codexConfig, "[mcp_servers.docs]\ncommand = \"npx\"\nargs = [\"-y\", \"docs-mcp\", \"--extra\"]\n")

	changes, err := planErr(t, m)
	if err == nil {
		t.Fatal("plan succeeded")
	}
	for _, want := range []string{
		`claude: mcp: server "docs" is already in ~/.claude.json and differs from zakwas.yaml in args, command, env; remove it with`,
		`codex: mcp: server "docs" is already in ~/codex/config.toml and differs from zakwas.yaml in args; remove it with`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks a value: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %v", targets(changes))
	}
	if readFile(t, cfg) != claudeBefore {
		t.Error("claude config changed")
	}
}

func TestMCPFailureStaysWithItsProvider(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{"docs": stdioServer("npx")})
	writeFile(t, filepath.Join(m.Paths.Home, ".claude.json"), `{"mcpServers": {"docs": {"command": "other"}}}`)
	changes, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), "claude: mcp: ") {
		t.Fatalf("error = %v", err)
	}
	if got, want := targets(changes), []string{"+ codex mcp docs (stdio npx)"}; !slices.Equal(got, want) {
		t.Errorf("changes = %q, want %q", got, want)
	}
}

func TestMCPForgetsServersChangedByHand(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"a": stdioServer("a"), "b": stdioServer("b")})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	apply(t, plan(t, m))

	writeFile(t, cfg, `{"mcpServers": {"b": {"type": "stdio", "command": "edited"}}}`)
	m.Agents.MCP = nil
	changes := plan(t, m)
	want := []string{"~ claude mcp a (already removed, stop tracking it)", "~ claude mcp b (changed since zakwas added it, leave it and stop tracking it)"}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got := readFile(t, cfg); !strings.Contains(got, "edited") {
		t.Errorf("edited server removed: %s", got)
	}
	if _, err := os.Stat(m.mcpStatePath()); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
	assertConverged(t, m)
}

func TestMCPOAuthLoginHint(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{
		"linear": {URL: "https://mcp.example.com/mcp", OAuth: config.MCPOAuth{Enabled: true}},
	})
	changes := plan(t, m)
	want := []string{
		"+ claude mcp linear (http https://mcp.example.com/mcp, then sign in with /mcp in Claude Code)",
		"+ codex mcp linear (http https://mcp.example.com/mcp, then sign in with `codex mcp login linear`)",
	}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if len(m.Runner.(*runnertest.Fake).Calls) != 0 {
		t.Errorf("plan ran %v", m.Runner.(*runnertest.Fake).Lines())
	}
	apply(t, changes)
	assertConverged(t, m)
}

func TestMCPCodexOptIn(t *testing.T) {
	m := mcpModule(t, nil, map[string]config.MCPServer{"a": stdioServer("a"), "b": {Command: "b", Providers: []string{config.ProviderCodex}}})
	m.Agents.Providers = nil
	if got, want := targets(plan(t, m)), []string{"+ claude mcp a (stdio a)", "+ codex mcp b (stdio b)"}; !slices.Equal(got, want) {
		t.Errorf("plan = %q, want %q", got, want)
	}
	m.Agents.Providers = []string{config.ProviderCodex}
	if got, want := targets(plan(t, m)), []string{"+ codex mcp a (stdio a)", "+ codex mcp b (stdio b)"}; !slices.Equal(got, want) {
		t.Errorf("named plan = %q, want %q", got, want)
	}
}

const codexBefore = `# Codex settings
model = "gpt-5"   # keep

[mcp_servers.keep]  # mine
command = "keep"
note = """
[mcp_servers.fake]
"""

# the docs server
[mcp_servers.docs]
command = "npx"
args = ["-y", "docs-mcp"]
[mcp_servers.docs.env]
LANG = "C"

[projects."/x"]
trust_level = "trusted"
# trailing comment
`

const codexWeb = `
[mcp_servers.web]
url = "https://example.com/mcp"
bearer_token_env_var = "WEB_TOKEN"

[mcp_servers.web.http_headers]
X-Static = "1"

[mcp_servers.web.env_http_headers]
X-Org = "ORG"
`

func TestCodexMCPEditsInPlace(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{
		"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"LANG": "C"}},
		"web":  {URL: "https://example.com/mcp", BearerTokenEnv: "WEB_TOKEN", HeaderEnv: map[string]string{"X-Org": "ORG"}, Headers: map[string]string{"X-Static": "1"}},
	})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	writeFile(t, cfg, codexBefore)

	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ codex mcp docs (adopt the server already there)", "+ codex mcp web (http https://example.com/mcp)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got, want := readFile(t, cfg), codexBefore+codexWeb; got != want {
		t.Fatalf("config.toml =\n%s\nwant\n%s", got, want)
	}
	assertConverged(t, m)

	docs := m.Agents.MCP["docs"]
	docs.Args = []string{"-y", "docs-mcp@2"}
	docs.EnvVars = []string{"DOCS_TOKEN"}
	m.Agents.MCP["docs"] = docs
	changes = plan(t, m)
	if got, want := targets(changes), []string{"~ codex mcp docs (stdio npx)"}; !slices.Equal(got, want) {
		t.Fatalf("update plan = %q, want %q", got, want)
	}
	apply(t, changes)
	afterDocs := strings.Replace(codexBefore, "args = [\"-y\", \"docs-mcp\"]\n[mcp_servers.docs.env]", "args = [\"-y\", \"docs-mcp@2\"]\nenv_vars = [\"DOCS_TOKEN\"]\n\n[mcp_servers.docs.env]", 1)
	if got, want := readFile(t, cfg), afterDocs+codexWeb; got != want {
		t.Fatalf("config.toml after update =\n%s\nwant\n%s", got, want)
	}
	assertConverged(t, m)

	web := m.Agents.MCP["web"]
	web.URL = "https://example.com/v2"
	m.Agents.MCP["web"] = web
	changes = plan(t, m)
	if got, want := targets(changes), []string{"~ codex mcp web (http https://example.com/v2)"}; !slices.Equal(got, want) {
		t.Fatalf("web update plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got, want := readFile(t, cfg), afterDocs+strings.Replace(codexWeb, "mcp\"", "v2\"", 1); got != want {
		t.Fatalf("config.toml after web update =\n%s\nwant\n%s", got, want)
	}

	delete(m.Agents.MCP, "web")
	apply(t, plan(t, m))
	if got := readFile(t, cfg); got != afterDocs {
		t.Errorf("config.toml after removing web =\n%s\nwant\n%s", got, afterDocs)
	}

	m.Agents.MCP = nil
	changes = plan(t, m)
	if got, want := targets(changes), []string{"- codex mcp docs (no longer in zakwas.yaml)"}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	apply(t, changes)
	wantGone := strings.Replace(afterDocs, `[mcp_servers.docs]
command = "npx"
args = ["-y", "docs-mcp@2"]
env_vars = ["DOCS_TOKEN"]

[mcp_servers.docs.env]
LANG = "C"

`, "", 1)
	if got := readFile(t, cfg); got != wantGone {
		t.Errorf("config.toml after removing docs =\n%s\nwant\n%s", got, wantGone)
	}
	assertConverged(t, m)
}

func TestCodexMCPRemovalRestoresAppendedFile(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	before := "model = \"x\"\n\n[tui]\nnotifications = true\n"
	writeFile(t, cfg, before)
	apply(t, plan(t, m))
	m.Agents.MCP = nil
	apply(t, plan(t, m))
	if got := readFile(t, cfg); got != before {
		t.Errorf("config.toml = %q, want %q", got, before)
	}
}

func TestCodexMCPCreatesConfig(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	apply(t, plan(t, m))
	cfg := filepath.Join(m.CodexHome, "config.toml")
	if got, want := readFile(t, cfg), "[mcp_servers.a]\ncommand = \"a\"\n"; got != want {
		t.Errorf("config.toml = %q, want %q", got, want)
	}
	if info, err := os.Stat(cfg); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", info, err)
	}
}

func TestCodexMCPInlineTable(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	writeFile(t, cfg, "[mcp_servers]\na = { command = \"old\" }\n")
	_, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), "differs from zakwas.yaml in command") {
		t.Fatalf("differing inline server: %v", err)
	}

	writeFile(t, cfg, "[mcp_servers]\na = { command = \"a\" }\n")
	apply(t, plan(t, m))
	m.Agents.MCP["a"] = stdioServer("a", "--new")
	if _, err := planErr(t, m); err == nil || !strings.Contains(err.Error(), `"a" is defined inline in ~/codex/config.toml, which zakwas can't edit`) {
		t.Fatalf("editing inline server: %v", err)
	}
	if got := readFile(t, cfg); got != "[mcp_servers]\na = { command = \"a\" }\n" {
		t.Errorf("config.toml = %q", got)
	}
}

func TestMCPReadOnlyConfig(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	for path, body := range map[string]string{filepath.Join(m.Paths.Home, ".claude.json"): "{}", filepath.Join(m.CodexHome, "config.toml"): ""} {
		writeFile(t, path, body)
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	_, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), "~/.claude.json: read-only") || !strings.Contains(err.Error(), "~/codex/config.toml: read-only") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexMCPKeepsConcurrentChange(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	writeFile(t, cfg, "model = \"x\"\n")
	changes := plan(t, m)
	writeBetween(t, "model = \"x\"\n# edited meanwhile\n")
	apply(t, changes)
	if got, want := readFile(t, cfg), "model = \"x\"\n# edited meanwhile\n\n[mcp_servers.a]\ncommand = \"a\"\n"; got != want {
		t.Errorf("config.toml = %q, want %q", got, want)
	}
}

func TestClaudeMCPKeepsConcurrentChange(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	writeFile(t, cfg, `{"x": 1}`)
	changes := plan(t, m)
	writeBetween(t, `{"x": 1, "y": 2}`)
	apply(t, changes)
	if got := readFile(t, cfg); !strings.Contains(got, `"y": 2`) || !strings.Contains(got, `"a"`) {
		t.Errorf(".claude.json = %s", got)
	}
}

func TestMCPApplyReplacesServerAppearingMeanwhile(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"a": stdioServer("a")})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	changes := plan(t, m)
	writeFile(t, cfg, `{"mcpServers": {"a": {"command": "theirs"}}}`)
	err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), `server "a" appeared in ~/.claude.json since the plan`) {
		t.Fatalf("apply error = %v", err)
	}
	if got := readFile(t, cfg); !strings.Contains(got, "theirs") {
		t.Errorf(".claude.json = %s", got)
	}
}

func TestMCPSymlinkedConfigIsEditedThrough(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"a": stdioServer("a")})
	target := filepath.Join(m.Paths.Root, "config.toml")
	writeFile(t, target, "model = \"x\"\n")
	if err := os.MkdirAll(m.CodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(m.CodexHome, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink replaced: %v, %v", info, err)
	}
	if got := readFile(t, target); !strings.Contains(got, "[mcp_servers.a]") {
		t.Errorf("target = %q", got)
	}
}

func TestCodexMCPKeepsHandEditsOfOwnedServer(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"foo": stdioServer("foo")})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	apply(t, plan(t, m))
	edited := readFile(t, cfg) + "tool_timeout_sec = 90\n\n[mcp_servers.foo.tools.search]\napproval_mode = \"approve\"\n"
	writeFile(t, cfg, edited)

	_, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), `server "foo" in ~/codex/config.toml was changed since zakwas added it and differs from zakwas.yaml in tool_timeout_sec, tools`) {
		t.Fatalf("plan error = %v", err)
	}
	if readFile(t, cfg) != edited {
		t.Error("config.toml changed")
	}
}

func TestMCPKeepsCommentsAfterServer(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"foo": stdioServer("foo")})
	cfg := filepath.Join(m.CodexHome, "config.toml")
	before := "[mcp_servers.foo]\ncommand = \"foo\"\n\n# ---- profiles ----\n# keep me\n\n[profiles.x]\nmodel = \"y\"\n"
	writeFile(t, cfg, before)
	apply(t, plan(t, m))
	m.Agents.MCP["foo"] = stdioServer("foo", "--v2")
	apply(t, plan(t, m))
	want := "[mcp_servers.foo]\ncommand = \"foo\"\nargs = [\"--v2\"]\n\n# ---- profiles ----\n# keep me\n\n[profiles.x]\nmodel = \"y\"\n"
	if got := readFile(t, cfg); got != want {
		t.Errorf("config.toml after update = %q, want %q", got, want)
	}
	m.Agents.MCP = nil
	apply(t, plan(t, m))
	if got, want := readFile(t, cfg), "# ---- profiles ----\n# keep me\n\n[profiles.x]\nmodel = \"y\"\n"; got != want {
		t.Errorf("config.toml after removal = %q, want %q", got, want)
	}
}

func TestMCPDiffHidesURLSecrets(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex}, map[string]config.MCPServer{
		"web": {URL: "https://example.com/mcp?key=SECRET&team=acme"},
	})
	changes := plan(t, m)
	if len(changes) != 2 {
		t.Fatalf("changes = %v", targets(changes))
	}
	for _, c := range changes {
		if strings.Contains(c.Diff, "SECRET") || strings.Contains(c.Summary(), "SECRET") || !strings.Contains(c.Diff, "REDACTED") {
			t.Errorf("%s diff = %q", c, c.Diff)
		}
	}
	apply(t, changes)
	if !strings.Contains(readFile(t, filepath.Join(m.Paths.Home, ".claude.json")), "key=SECRET") {
		t.Error("the URL was not written as declared")
	}
	m.Agents.MCP = nil
	for _, c := range plan(t, m) {
		if strings.Contains(c.Diff, "SECRET") {
			t.Errorf("%s removal diff = %q", c, c.Diff)
		}
	}
}

func TestCodexMCPPromptsForTheApplyTool(t *testing.T) {
	zakwas := stdioServer("/opt/homebrew/bin/zakwas", "mcp", "--enable-apply")
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"zakwas": zakwas, "plain": stdioServer("zakwas", "mcp")})
	apply(t, plan(t, m))

	got := readFile(t, filepath.Join(m.CodexHome, "config.toml"))
	want := "[mcp_servers.zakwas.tools.zakwas_apply]\napproval_mode = \"prompt\"\n"
	if !strings.Contains(got, want) {
		t.Errorf("config.toml lacks the prompt:\n%s", got)
	}
	if strings.Count(got, "approval_mode") != 1 {
		t.Errorf("a server without --enable-apply prompts too:\n%s", got)
	}
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan has %d changes: %q", len(again), targets(again))
	}
}

func TestClaudeMCPEntryCarriesNoToolApproval(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{"zakwas": stdioServer("zakwas", "mcp", "--enable-apply")})
	apply(t, plan(t, m))
	got := readFile(t, filepath.Join(m.Paths.Home, ".claude.json"))
	if strings.Contains(got, "approval") {
		t.Errorf("claude.json mentions an approval mode:\n%s", got)
	}
}
