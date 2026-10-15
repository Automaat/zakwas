package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const (
	copilotMarketList = "copilot plugin marketplace list --json"
	copilotPluginList = "copilot plugin list --json"
	copilotDefaults   = `{"name":"copilot-plugins","source":"GitHub: github/copilot-plugins","isDefault":true},{"name":"awesome-copilot","source":"GitHub: github/awesome-copilot","isDefault":true}`
)

func newCopilotModule(t *testing.T, a config.Agents) (*Module, *runnertest.Fake) {
	t.Helper()
	a.Providers = []string{config.ProviderCopilot}
	m, fake := newModule(t, a)
	m.CopilotHome = filepath.Join(t.TempDir(), "copilot")
	return m, fake
}

func assertCopilotCalls(t *testing.T, m *Module, fake *runnertest.Fake) {
	t.Helper()
	want := []string{"COPILOT_HOME=" + m.CopilotHome}
	for _, c := range fake.Calls {
		if c.Name != "copilot" || c.Dir != "/" || !reflect.DeepEqual(c.Env, want) {
			t.Errorf("%s: dir %q env %v, want copilot from / with %v", c, c.Dir, c.Env, want)
		}
	}
}

func assertCopilotReadOnly(t *testing.T, fake *runnertest.Fake) {
	t.Helper()
	for _, l := range fake.Lines() {
		if l != copilotMarketList && l != copilotPluginList && !strings.HasPrefix(l, "copilot plugin marketplace browse ") {
			t.Errorf("plan ran %q", l)
		}
	}
}

func TestCopilotConvergeFromScratch(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{
			"sai":   {Source: "o/sai#v1"},
			"local": {Source: "./mp"},
		},
		Plugins: []config.Plugin{{ID: "humanize@sai"}},
	})
	localDir := filepath.Join(m.Paths.Root, "mp")
	fake.OnOK(copilotMarketList, `[`+copilotDefaults+`]`)
	fake.OnOK(copilotPluginList, `[]`)

	changes := plan(t, m)
	want := []string{
		"+ copilot marketplace local (" + localDir + ")",
		"+ copilot marketplace sai (o/sai#v1)",
		"+ copilot plugin humanize@sai",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	assertCopilotReadOnly(t, fake)

	fake.OnOK("copilot plugin marketplace add "+localDir, `Marketplace "local" added successfully.`)
	fake.OnOK("copilot plugin marketplace add o/sai#v1", `Marketplace "sai" added successfully.`)
	fake.OnOK("copilot plugin install humanize@sai", `Plugin "humanize" installed successfully.`)
	apply(t, changes)
	assertCopilotCalls(t, m, fake)

	writeFile(t, filepath.Join(m.CopilotHome, "settings.json"), `{"extraKnownMarketplaces":{"sai":{"source":{"source":"github","repo":"o/sai","ref":"v1"}}}}`)
	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: o/sai","isDefault":false},{"name":"local","source":"Local: `+localDir+`","isDefault":false},`+copilotDefaults+`]`)
	fake.OnOK(copilotPluginList, `[{"name":"humanize","marketplace":"sai","enabled":true,"source":"installed"}]`)
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan not empty: %v", targets(again))
	}
	assertCopilotReadOnly(t, fake)
}

func TestCopilotNeverWritesItsSettings(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "humanize@sai"}},
		Instructions: config.InstructionFiles{"AGENTS.md"},
		MCP:          map[string]config.MCPServer{"docs": stdioServer("docs-mcp")},
		Hooks:        &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/notify"}}},
	})
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "be terse\n")
	settings := filepath.Join(m.CopilotHome, "settings.json")
	state := filepath.Join(m.CopilotHome, "config.json")
	writeFile(t, settings, `{"theme":"dark"}`)
	writeFile(t, state, `{"trustedFolders":["/x"]}`)
	fake.OnOK(copilotMarketList, `[`+copilotDefaults+`]`)
	fake.OnOK(copilotPluginList, `[]`)
	fake.OnOK("copilot plugin marketplace add o/sai", `Marketplace "sai" added successfully.`)
	fake.OnOK("copilot plugin install humanize@sai", `Plugin "humanize" installed successfully.`)
	apply(t, plan(t, m))
	if got := readFile(t, settings); got != `{"theme":"dark"}` {
		t.Errorf("settings.json = %q", got)
	}
	if got := readFile(t, state); got != `{"trustedFolders":["/x"]}` {
		t.Errorf("config.json = %q", got)
	}
}

func TestCopilotIsOptIn(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "humanize@sai"}},
		Instructions: config.InstructionFiles{"AGENTS.md"},
		MCP:          map[string]config.MCPServer{"docs": stdioServer("docs-mcp")},
		Hooks:        &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/notify"}}},
		Skills:       []config.Skill{{Path: "skills/mine"}},
		Profiles:     map[string]config.Profile{"rev": {Description: "reviews"}},
	})
	m.Agents.Providers = nil
	m.CopilotHome = filepath.Join(t.TempDir(), "copilot")
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "be terse\n")
	localSkill(t, m, "skills/mine", "mine")
	fake.Missing(config.ProviderCodex, config.ProviderOpencode)
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)

	changes, _ := m.Plan(t.Context())
	for _, c := range changes {
		if c.Group == config.ProviderCopilot || strings.Contains(c.Target, "copilot") || strings.Contains(c.Target, m.CopilotHome) {
			t.Errorf("unnamed copilot was planned: %s %s", c.Action, c.Target)
		}
	}
	for _, l := range fake.Lines() {
		if strings.HasPrefix(l, "copilot") {
			t.Errorf("ran %q", l)
		}
	}
}

func TestCopilotEnablesAndRejects(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "a@sai"}, {ID: "b@sai"}},
	})
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: o/sai","isDefault":false}]`)
	fake.OnOK(copilotPluginList, `[{"name":"a","marketplace":"sai","enabled":false,"source":"installed"}]`)
	fake.OnOK("copilot plugin marketplace browse sai --json", `[{"name":"a","marketplace":"sai"}]`)
	_, err := m.Plan(t.Context())
	if err == nil || !strings.Contains(err.Error(), `plugin "b" is not in marketplace "sai"`) {
		t.Fatalf("err = %v", err)
	}

	m.Agents.Plugins = m.Agents.Plugins[:1]
	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ copilot plugin a@sai (enable)"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	fake.OnOK("copilot plugin enable a@sai", `Enabled plugin "a@sai".`)
	apply(t, changes)
}

func TestCopilotRejectsAnotherSource(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}}})
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: x/other","isDefault":false}]`)
	fake.OnOK(copilotPluginList, `[]`)
	_, err := m.Plan(t.Context())
	if err == nil || !strings.Contains(err.Error(), `marketplace "sai" comes from x/other, but zakwas.yaml declares o/sai`) {
		t.Fatalf("err = %v", err)
	}

	writeFile(t, filepath.Join(m.CopilotHome, "settings.json"), `{"extraKnownMarketplaces":{"sai":{"source":{"source":"github","repo":"o/sai","ref":"old"}}}}`)
	fake = runnertest.New()
	m.Runner = fake
	m.Agents.Marketplaces["sai"] = config.Marketplace{Source: "o/sai#new"}
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: o/sai","isDefault":false}]`)
	fake.OnOK(copilotPluginList, `[]`)
	if _, err := m.Plan(t.Context()); err == nil || !strings.Contains(err.Error(), "declares o/sai#new") {
		t.Fatalf("ref change err = %v", err)
	}
}

func TestCopilotPrune(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "keep@sai"}},
	})
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: o/sai","isDefault":false},{"name":"old","source":"URL: https://example.com/old.git","isDefault":false},{"name":"live","source":"Local: /x/live","isDefault":false},`+copilotDefaults+`]`)
	fake.OnOK(copilotPluginList, `[
		{"name":"keep","marketplace":"sai","enabled":true,"source":"installed"},
		{"name":"stale","marketplace":"sai","enabled":true,"source":"installed"},
		{"name":"gone","marketplace":"old","enabled":true,"source":"installed"},
		{"name":"off","marketplace":"live","enabled":false,"source":"live","version":"1.0.0"},
		{"name":"builtin","marketplace":"copilot-plugins","enabled":true,"source":"installed"},
		{"name":"direct","enabled":true,"source":"installed"}]`)

	changes := plan(t, m)
	want := []string{
		"- copilot plugin gone@old",
		"- copilot plugin stale@sai",
		"- copilot marketplace live (/x/live)",
		"- copilot marketplace old (https://example.com/old.git)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, c := range changes {
		if !c.Destructive {
			t.Errorf("%s is not destructive", c.Target)
		}
	}
	for _, l := range []string{"copilot plugin uninstall gone@old", "copilot plugin uninstall stale@sai", "copilot plugin marketplace remove live", "copilot plugin marketplace remove old"} {
		fake.OnOK(l, "ok")
	}
	apply(t, changes)
	assertCopilotCalls(t, m, fake)
}

func TestCopilotRefreshUpdatesMarketplacesAndPlugins(t *testing.T) {
	m, fake := newCopilotModule(t, config.Agents{
		Upgrade:      true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}, "new": {Source: "o/new"}},
		Plugins:      []config.Plugin{{ID: "a@sai"}, {ID: "live@sai"}},
	})
	fake.OnOK(copilotMarketList, `[{"name":"sai","source":"GitHub: o/sai","isDefault":false}]`)
	fake.OnOK(copilotPluginList, `[{"name":"a","marketplace":"sai","enabled":true,"source":"installed"},{"name":"live","marketplace":"sai","enabled":true,"source":"live"},{"name":"other","marketplace":"sai","enabled":true,"source":"installed"}]`)
	fake.OnOK("copilot plugin marketplace update sai", "ok")
	fake.OnOK("copilot plugin update a@sai", "ok")
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{copilotMarketList, copilotPluginList, "copilot plugin marketplace update sai", "copilot plugin update a@sai"}
	if got := fake.Lines(); !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
	assertCopilotCalls(t, m, fake)

	m.Agents.Upgrade = false
	fake.Calls = nil
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fake.Lines(), "copilot plugin update a@sai") {
		t.Errorf("updated a plugin without agents.upgrade: %v", fake.Lines())
	}
}

func TestCopilotMissingCLIOnlyFailsCopilot(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Providers:    []string{config.ProviderClaude, config.ProviderCopilot},
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "humanize@sai"}},
	})
	m.CopilotHome = filepath.Join(t.TempDir(), "copilot")
	fake.Missing("copilot")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": [{"pluginId": "humanize@sai", "version": "2.5.0"}]}`)
	changes, err := m.Plan(t.Context())
	if err == nil || !strings.Contains(err.Error(), "copilot: Copilot CLI is not installed") {
		t.Fatalf("err = %v", err)
	}
	if got := targets(changes); !slices.Contains(got, "+ claude plugin humanize@sai (→ 2.5.0)") {
		t.Errorf("claude changes lost: %v", got)
	}
}

func TestCopilotApplyFailureStopsOnlyCopilot(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Providers:    []string{config.ProviderClaude, config.ProviderCopilot},
		Instructions: config.InstructionFiles{"AGENTS.md"},
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai", Providers: []string{config.ProviderCopilot}}},
		Plugins:      []config.Plugin{{ID: "humanize@sai"}},
	})
	m.CopilotHome = filepath.Join(t.TempDir(), "copilot")
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude")
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "be terse\n")
	fake.OnOK(copilotMarketList, `[`+copilotDefaults+`]`)
	fake.OnOK(copilotPluginList, `[]`)
	fake.On("copilot plugin marketplace add o/sai", runner.Result{ExitCode: 1, Stderr: "Failed to add marketplace: network"})

	changes := plan(t, m)
	if err := runApply(changes); err == nil || !strings.Contains(err.Error(), "network") {
		t.Fatalf("apply err = %v", err)
	}
	assertCopy(t, filepath.Join(m.ClaudeConfigDir, "CLAUDE.md"), "be terse\n")
	assertCopy(t, filepath.Join(m.CopilotHome, "instructions", "zakwas.instructions.md"), "---\napplyTo: \"**\"\n---\n\nbe terse\n")
}

func TestCopilotInstructions(t *testing.T) {
	m, _ := newCopilotModule(t, config.Agents{Instructions: config.InstructionFiles{"AGENTS.md"}})
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "be terse\n")
	dst := filepath.Join(m.CopilotHome, "instructions", "zakwas.instructions.md")

	changes := plan(t, m)
	if got, want := groups(changes), []string{"copilot: + " + m.Paths.Pretty(dst)}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	apply(t, changes)
	assertCopy(t, dst, "---\napplyTo: \"**\"\n---\n\nbe terse\n")
	assertConverged(t, m)

	m.Agents.Instructions = nil
	changes = plan(t, m)
	if got, want := groups(changes), []string{"instructions: - " + m.Paths.Pretty(dst)}; !slices.Equal(got, want) {
		t.Fatalf("removal = %v, want %v", got, want)
	}
	apply(t, changes)
	assertGone(t, dst)
}

func TestCopilotCommandHooks(t *testing.T) {
	m, _ := newCopilotModule(t, config.Agents{Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{
		{Event: "beforeTool", Command: "/bin/guard"},
		{Event: "afterTool", Command: "/bin/audit"},
		{Event: "sessionStart", Command: "/bin/hello"},
		{Event: "stop", Command: "/bin/bye"},
		{Event: "stop", Command: "/bin/other"},
	}}})
	dst := filepath.Join(m.CopilotHome, "hooks", "zakwas.json")
	changes := plan(t, m)
	if got, want := groups(changes), []string{"copilot: + " + m.Paths.Pretty(dst)}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	apply(t, changes)
	want := `{
  "version": 1,
  "hooks": {
    "agentStop": [
      {
        "type": "command",
        "bash": "/bin/bye"
      },
      {
        "type": "command",
        "bash": "/bin/other"
      }
    ],
    "postToolUse": [
      {
        "type": "command",
        "bash": "/bin/audit"
      }
    ],
    "preToolUse": [
      {
        "type": "command",
        "bash": "/bin/guard"
      }
    ],
    "sessionStart": [
      {
        "type": "command",
        "bash": "/bin/hello"
      }
    ]
  }
}
`
	assertCopy(t, dst, want)
	assertConverged(t, m)

	m.Agents.Hooks = nil
	changes = plan(t, m)
	if got, want := targets(changes), []string{"- " + m.Paths.Pretty(dst) + " (hooks no longer declared)"}; !slices.Equal(got, want) {
		t.Fatalf("removal = %v, want %v", got, want)
	}
	apply(t, changes)
	assertGone(t, dst)
	assertConverged(t, m)
}

func TestCopilotMCPLifecycle(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCopilot}, map[string]config.MCPServer{
		"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"LANG": "C"}, EnvVars: []string{"DOCS_TOKEN"}},
		"web":  {URL: "https://example.com/mcp", BearerTokenEnv: "WEB_TOKEN", HeaderEnv: map[string]string{"X-Org": "ORG"}},
		"same": {Command: "same-mcp"},
	})
	m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
	cfg := filepath.Join(m.CopilotHome, "mcp-config.json")
	writeFile(t, cfg, `{
  "mcpServers": {
    "mine": {"tools": ["*"], "type": "local", "command": "mine"},
    "same": {"tools": ["*"], "type": "local", "command": "same-mcp", "args": []}
  }
}`)

	changes := plan(t, m)
	want := []string{"+ copilot mcp docs (stdio npx)", "~ copilot mcp same (adopt the server already there)", "+ copilot mcp web (http https://example.com/mcp)"}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	got := readFile(t, cfg)
	for _, frag := range []string{
		`"mine": {`, `"command": "mine"`,
		`"type": "local"`, `"DOCS_TOKEN": "${DOCS_TOKEN}"`, `"LANG": "C"`,
		`"type": "http"`, `"Authorization": "Bearer ${WEB_TOKEN}"`, `"X-Org": "${ORG}"`, `"tools": [`,
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("mcp-config.json lacks %s:\n%s", frag, got)
		}
	}
	assertConverged(t, m)

	delete(m.Agents.MCP, "web")
	delete(m.Agents.MCP, "same")
	changes = plan(t, m)
	if got, want := targets(changes), []string{"- copilot mcp same (no longer in zakwas.yaml)", "- copilot mcp web (no longer in zakwas.yaml)"}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	apply(t, changes)
	got = readFile(t, cfg)
	if !strings.Contains(got, `"mine"`) || strings.Contains(got, `"web"`) || strings.Contains(got, `"same"`) || !strings.Contains(got, `"docs"`) {
		t.Errorf("mcp-config.json after removal:\n%s", got)
	}
}

func TestCopilotMCPRefusesADifferingServer(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCopilot}, map[string]config.MCPServer{"docs": stdioServer("docs-mcp")})
	m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
	writeFile(t, filepath.Join(m.CopilotHome, "mcp-config.json"), `{"mcpServers":{"docs":{"tools":["*"],"type":"local","command":"other"}}}`)
	_, err := planErr(t, m)
	if err == nil || !strings.Contains(err.Error(), `server "docs" is already in`) || !strings.Contains(err.Error(), "copilot mcp remove docs") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopilotSkillsDir(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Providers: []string{config.ProviderCopilot}, Skills: []config.Skill{{Path: "skills/mine"}}})
	mine := localSkill(t, m, "skills/mine", "mine")
	if got, want := targets(plan(t, m)), []string{"+ ~/.agents/skills/mine (→ " + mine + ")"}; !slices.Equal(got, want) {
		t.Errorf("default dir: got %v, want %v", got, want)
	}
	m.CopilotHome = filepath.Join(m.Paths.Home, "cp")
	changes := plan(t, m)
	if got, want := targets(changes), []string{"+ ~/cp/skills/mine (→ " + mine + ")"}; !slices.Equal(got, want) {
		t.Errorf("COPILOT_HOME: got %v, want %v", got, want)
	}
	apply(t, changes)
	if link, err := os.Readlink(filepath.Join(m.CopilotHome, "skills", "mine")); err != nil || link != mine {
		t.Errorf("link = %q, %v", link, err)
	}
	assertConverged(t, m)
}

func TestCopilotProfiles(t *testing.T) {
	no := false
	m, _ := newCopilotModule(t, config.Agents{
		MCP: map[string]config.MCPServer{"docs": stdioServer("docs-mcp"), "web": {URL: "https://example.com/mcp"}},
		Profiles: map[string]config.Profile{
			"reviewer": {
				Description: "Reviews code",
				Prompt:      "reviewer.md",
				Tools:       []string{"read", "search", "shell"},
				Edit:        config.PermissionDeny,
				Shell:       "ask",
				Network:     &no,
				MCP:         map[string]string{"docs": "ask"},
				Sandbox:     "read-only",
				Reviewer:    "auto",
				Copilot:     &config.ProfileProvider{Model: "gpt-5", Overrides: map[string]any{"disable-model-invocation": true}},
			},
			"open": {Description: "Everything", MCP: map[string]string{"docs": "allow", "web": "ask"}, Edit: config.PermissionAllow},
		},
	})
	writeFile(t, filepath.Join(m.Paths.Root, "reviewer.md"), "You review.\n")
	changes := plan(t, m)
	if got := targets(changes); len(got) != 4 {
		t.Fatalf("got %v", got)
	}
	apply(t, changes)
	assertCopy(t, filepath.Join(m.CopilotHome, "agents", "open.agent.md"), "---\nname: open\ndescription: Everything\n---\n")
	assertCopy(t, filepath.Join(m.CopilotHome, "agents", "reviewer.agent.md"), `---
name: reviewer
description: Reviews code
model: gpt-5
tools:
  - read
  - search
  - execute
  - docs/*
disable-model-invocation: true
---

You review.
`)
	assertConverged(t, m)

	got := m.Warnings(t.Context())
	want := []string{
		"copilot profile open can't express: edit: allow; mcp docs: allow",
		"copilot profile reviewer can't express: sandbox; reviewer: auto; network: false for shell commands",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("warnings = %q\nwant %q", got, want)
	}
}

func TestCopilotProfileWithADeniedServerListsTools(t *testing.T) {
	m, _ := newCopilotModule(t, config.Agents{
		MCP:      map[string]config.MCPServer{"docs": stdioServer("docs-mcp")},
		Profiles: map[string]config.Profile{"quiet": {Description: "No servers"}},
	})
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.CopilotHome, "agents", "quiet.agent.md"), `---
name: quiet
description: No servers
tools:
  - read
  - search
  - edit
  - execute
  - web
---
`)
}

func TestCopilotPermissionsAreUnsupportedNotAnError(t *testing.T) {
	m, _ := newCopilotModule(t, config.Agents{Permissions: &config.AgentPermissions{
		Allow: []config.AgentPermission{{Command: "git status"}, {Command: "ls", Providers: []string{config.ProviderClaude}}},
		Deny:  []config.AgentPermission{{Command: "rm -rf"}},
	}})
	m.Agents.Providers = []string{config.ProviderClaude, config.ProviderCopilot}
	m.Guard = true
	changes, err := m.Plan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Group == config.ProviderCopilot {
			t.Errorf("planned for copilot: %s", c.Target)
		}
	}
	var gotPerm, gotGuard bool
	for _, w := range m.Warnings(t.Context()) {
		gotPerm = gotPerm || strings.Contains(w, "copilot can't express agents.permissions (2 for copilot)")
		gotGuard = gotGuard || strings.Contains(w, "copilot can't be protected by rules")
	}
	if !gotPerm || !gotGuard {
		t.Errorf("warnings = %q", m.Warnings(t.Context()))
	}
}
