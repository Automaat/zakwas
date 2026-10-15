package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

// geminiRunner stands in for gemini: link and uninstall keep the record
// gemini makes of a linked extension.
type geminiRunner struct {
	*runnertest.Fake
	m *Module
}

func (r *geminiRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name == "gemini" && len(c.Args) >= 2 && c.Args[0] == "extensions" {
		r.Calls = append(r.Calls, c)
		base := r.m.Paths.Home
		for _, e := range c.Env {
			if v, ok := strings.CutPrefix(e, "GEMINI_CLI_HOME="); ok {
				base = v
			}
		}
		switch c.Args[1] {
		case "link":
			data, _ := json.Marshal(geminiInstall{Source: c.Args[2], Type: "link"})
			path := geminiInstallPathAt(base)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return runner.Result{}, err
			}
			return runner.Result{}, os.WriteFile(path, data, 0o644)
		case "uninstall":
			if _, err := os.Stat(filepath.Join(base, ".gemini", geminiExtension, "gemini-extension.json")); err != nil {
				return runner.Result{}, err
			}
			return runner.Result{}, os.RemoveAll(filepath.Dir(geminiInstallPathAt(base)))
		}
	}
	return r.Fake.Run(ctx, c)
}

func newGemini(t *testing.T, a config.Agents) (*Module, *geminiRunner) {
	t.Helper()
	if a.Providers == nil {
		a.Providers = []string{config.ProviderGemini}
	}
	home := t.TempDir()
	m := &Module{Agents: a, Paths: config.Paths{Home: home, Root: t.TempDir()}, ClaudeConfigDir: filepath.Join(home, ".claude")}
	r := &geminiRunner{Fake: runnertest.New(), m: m}
	m.Runner = r
	return m, r
}

func geminiRules(t *testing.T, m *Module) []geminiRule {
	t.Helper()
	var doc struct {
		Rule []struct {
			ToolName      any
			Subagent      string
			MCPName       string
			CommandPrefix string
			CommandRegex  string
			Decision      string
			Priority      int
			DenyMessage   string
		}
	}
	if err := toml.Unmarshal([]byte(readFile(t, m.geminiPolicyPath())), &doc); err != nil {
		t.Fatal(err)
	}
	var out []geminiRule
	for _, r := range doc.Rule {
		out = append(out, geminiRule{
			ToolName: r.ToolName, Subagent: r.Subagent, MCPName: r.MCPName, CommandPrefix: r.CommandPrefix,
			CommandRegex: r.CommandRegex, Decision: r.Decision, Priority: r.Priority, DenyMessage: r.DenyMessage,
		})
	}
	return out
}

func geminiConfig(t *testing.T) config.Agents {
	t.Helper()
	return config.Agents{
		Instructions: config.InstructionFiles{"INSTR.md"},
		MCP: map[string]config.MCPServer{
			"docs": {Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"MODE": "ro"}, EnvVars: []string{"DOCS_REGION"}},
			"web":  {URL: "https://example.com/mcp", Headers: map[string]string{"X-Team": "a"}, HeaderEnv: map[string]string{"X-Region": "REGION"}},
		},
		Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{
			{Event: "beforeTool", Command: "/bin/before"},
			{Event: "afterTool", Command: "/bin/after"},
			{Event: "sessionStart", Command: "/bin/start"},
			{Event: "stop", Command: "/bin/stop"},
		}},
		Permissions: &config.AgentPermissions{
			Allow:  []config.AgentPermission{{Command: "git status"}},
			Review: []config.AgentPermission{{Command: "git push"}},
			Deny:   []config.AgentPermission{{Command: "git push --force", Justification: "use --force-with-lease"}},
		},
		Skills: []config.Skill{{Path: "skills/mine"}},
		Profiles: map[string]config.Profile{
			"review": {
				Description: "Read-only review", Prompt: "review.md", Tools: []string{"read", "search", "shell"}, Edit: "deny", Shell: "ask",
				MCP: map[string]string{"docs": "allow"}, Gemini: &config.ProfileProvider{Model: "gemini-2.5-pro", Overrides: map[string]any{"temperature": 0.2}},
			},
		},
	}
}

func prepareGemini(t *testing.T, m *Module) {
	t.Helper()
	writeFile(t, filepath.Join(m.Paths.Root, "INSTR.md"), "Hello {{ .Provider }}\n")
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "Review as {{ .Provider }}.\n")
	writeFile(t, filepath.Join(m.Paths.Root, "skills", "mine", "SKILL.md"), skillMD("mine"))
}

func TestGeminiConvergesTheExtension(t *testing.T) {
	m, r := newGemini(t, geminiConfig(t))
	prepareGemini(t, m)

	changes := plan(t, m)
	if len(r.Calls) != 0 {
		t.Errorf("plan ran %v", r.Lines())
	}
	for _, c := range changes {
		if c.Group != config.ProviderGemini {
			t.Errorf("%s is in group %q, want gemini", c.Target, c.Group)
		}
	}
	last := changes[len(changes)-1]
	if last.Target != "gemini extension zakwas" || last.Action != engine.Create {
		t.Errorf("last change = %v, want the extension link, after the files it needs", last)
	}
	apply(t, changes)

	ext := filepath.Join(m.Paths.Home, ".gemini", "zakwas")
	if got := readFile(t, filepath.Join(ext, "GEMINI.md")); got != "Hello gemini\n" {
		t.Errorf("GEMINI.md = %q", got)
	}
	var manifest geminiManifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(ext, "gemini-extension.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "zakwas" || manifest.ContextFileName != "GEMINI.md" {
		t.Errorf("manifest = %+v", manifest)
	}
	docs := manifest.MCPServers["docs"]
	if docs.Command != "npx" || !slices.Equal(docs.Args, []string{"-y", "docs-mcp"}) || len(docs.Env) != 2 || docs.Env["DOCS_REGION"] != "$DOCS_REGION" || docs.Env["MODE"] != "ro" {
		t.Errorf("docs = %+v", docs)
	}
	web := manifest.MCPServers["web"]
	if web.HTTPURL != "https://example.com/mcp" || web.Headers["X-Region"] != "$REGION" || web.Headers["X-Team"] != "a" || web.Command != "" {
		t.Errorf("web = %+v", web)
	}

	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(ext, "hooks", "hooks.json"))), &hooks); err != nil {
		t.Fatal(err)
	}
	for event, command := range map[string]string{"BeforeTool": "/bin/before", "AfterTool": "/bin/after", "SessionStart": "/bin/start", "AfterAgent": "/bin/stop"} {
		groups := hooks.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || groups[0].Hooks[0].Command != command {
			t.Errorf("%s hooks = %+v, want %s", event, groups, command)
		}
	}
	if len(hooks.Hooks) != 4 {
		t.Errorf("hooks = %v", hooks.Hooks)
	}

	agent := readFile(t, filepath.Join(ext, "agents", "review.md"))
	for _, want := range []string{"name: review\n", "kind: local\n", "model: gemini-2.5-pro\n", "temperature: 0.2\n", "  - read_file\n", "  - run_shell_command\n", "  - mcp_docs_*\n", "\nReview as gemini.\n"} {
		if !strings.Contains(agent, want) {
			t.Errorf("review.md lacks %q:\n%s", want, agent)
		}
	}
	if strings.Contains(agent, "write_file") || strings.Contains(agent, "mcp_web") {
		t.Errorf("review.md lists tools the profile doesn't keep:\n%s", agent)
	}

	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "mine"), filepath.Join(m.Paths.Root, "skills", "mine"))
	for _, never := range []string{"settings.json", "GEMINI.md"} {
		if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".gemini", never)); !os.IsNotExist(err) {
			t.Errorf("zakwas wrote ~/.gemini/%s: %v", never, err)
		}
	}
	link := r.Calls[len(r.Calls)-1]
	if got := link.String(); got != "gemini extensions link "+ext+" --consent" || link.Dir != "/" || len(link.Env) != 0 {
		t.Errorf("link = %s in %q with %v", got, link.Dir, link.Env)
	}
	assertConverged(t, m)
}

func TestGeminiPolicyRanksDenyOverAskOverAllow(t *testing.T) {
	m, _ := newGemini(t, geminiConfig(t))
	m.Guard = true
	prepareGemini(t, m)
	apply(t, plan(t, m))

	byCommand := map[string]geminiRule{}
	var guard []geminiRule
	for _, r := range geminiRules(t, m) {
		if r.CommandRegex != "" {
			guard = append(guard, r)
		}
		if r.CommandPrefix != "" {
			byCommand[r.CommandPrefix] = r
		}
	}
	status, push, force := byCommand["git status"], byCommand["git push"], byCommand["git push --force"]
	if status.Decision != "allow" || push.Decision != "ask_user" || force.Decision != "deny" {
		t.Fatalf("decisions = %q %q %q", status.Decision, push.Decision, force.Decision)
	}
	if force.Priority <= push.Priority || push.Priority <= status.Priority {
		t.Errorf("priorities deny %d, ask %d, allow %d: want deny > ask > allow", force.Priority, push.Priority, status.Priority)
	}
	if force.DenyMessage != "use --force-with-lease" || push.DenyMessage != "" {
		t.Errorf("deny messages = %q, %q", force.DenyMessage, push.DenyMessage)
	}
	if len(guard) != len(config.GuardedCommands) {
		t.Fatalf("guard rules = %d, want %d", len(guard), len(config.GuardedCommands))
	}
	for _, r := range guard {
		if r.Decision != "deny" || r.Priority <= force.Priority {
			t.Errorf("guard rule %+v must deny above agents.permissions", r)
		}
	}
	for _, r := range geminiRules(t, m) {
		if r.Subagent != "" && r.Decision != "deny" && r.Priority >= status.Priority {
			t.Errorf("profile rule %+v outranks agents.permissions", r)
		}
	}
}

func TestGeminiGuardRegexMatchesWhatTheGuardDenies(t *testing.T) {
	regex := geminiGuardRegex([]string{"zakwas", "apply"})
	re := regexp.MustCompile(`"command":"` + regex)
	for command, want := range map[string]bool{
		"zakwas apply":                      true,
		"zakwas apply -y":                   true,
		"zakwas -c zakwas.yaml apply":       true,
		"~/.local/bin/zakwas apply":         true,
		"/opt/homebrew/bin/zakwas -y apply": true,
		"zakwas plan":                       false,
		"zakwas applyx":                     false,
		"notzakwas apply":                   false,
		"echo zakwas apply":                 false,
	} {
		encoded, _ := json.Marshal(map[string]string{"command": command})
		if got := re.Match(encoded); got != want {
			t.Errorf("%q matches = %v, want %v", command, got, want)
		}
	}
	if regexp.MustCompile(`\([^)]*[*+?{].*\)[*+?{]`).MatchString(regex) {
		t.Error("the guard regex has a quantified group, which Gemini rejects as unsafe")
	}
}

func TestGeminiGuardOffWritesNoGuardRules(t *testing.T) {
	m, _ := newGemini(t, config.Agents{Permissions: &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git status"}}}})
	apply(t, plan(t, m))
	rules := geminiRules(t, m)
	if len(rules) != 1 || rules[0].CommandPrefix != "git status" {
		t.Errorf("rules = %+v", rules)
	}
}

func TestGeminiIsOptIn(t *testing.T) {
	for _, providers := range [][]string{nil, {config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode}} {
		a := geminiConfig(t)
		m, r := newGemini(t, a)
		m.Agents.Providers = providers
		m.Agents.Instructions = nil
		prepareGemini(t, m)
		for _, c := range plan(t, m) {
			if c.Group == config.ProviderGemini || strings.Contains(c.Target, ".gemini") {
				t.Errorf("providers %v: planned %v", providers, c)
			}
		}
		if r.Ran("gemini") {
			t.Errorf("ran %v", r.Lines())
		}
	}
}

func TestGeminiNamedByAnEntryAlone(t *testing.T) {
	m, _ := newGemini(t, config.Agents{Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{
		{Event: "stop", Command: "/bin/gemini-stop", Providers: []string{config.ProviderGemini}},
		{Event: "stop", Command: "/bin/everyone"},
	}}})
	m.Agents.Providers = nil
	apply(t, plan(t, m))
	hooks := readFile(t, filepath.Join(m.geminiExtDir(), "hooks", "hooks.json"))
	if !strings.Contains(hooks, "/bin/gemini-stop") || strings.Contains(hooks, "/bin/everyone") {
		t.Errorf("hooks.json = %s", hooks)
	}
	if strings.Contains(readFile(t, filepath.Join(m.geminiExtDir(), "gemini-extension.json")), "contextFileName") {
		t.Error("manifest names a context file without instructions")
	}
	assertConverged(t, m)
}

func TestGeminiLeavesServersItCannotCarry(t *testing.T) {
	m, _ := newGemini(t, config.Agents{MCP: map[string]config.MCPServer{
		"ok":         {Command: "ok", EnvVars: []string{"REGION"}},
		"secret":     {Command: "ok", EnvVars: []string{"GITHUB_TOKEN"}},
		"bearer":     {URL: "https://example.com/mcp", BearerTokenEnv: "API_KEY"},
		"under_line": {Command: "x"},
		"dollar":     {Command: "x", Env: map[string]string{"A": "$HOME/x"}},
		"args":       {Command: "x", Args: []string{"--root", "${HOME}/data"}},
		"cmd":        {Command: "$BIN/x"},
		"url":        {URL: "https://example.com/$X"},
		"oauth":      {URL: "https://example.com/oauth", OAuth: true},
	}})
	apply(t, plan(t, m))
	var manifest geminiManifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(m.geminiExtDir(), "gemini-extension.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range manifest.MCPServers {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"bearer", "oauth", "ok", "secret"}) {
		t.Errorf("registered %v, want bearer, oauth, ok and secret: Gemini expands secret-named variables for extensions", names)
	}
	if got := manifest.MCPServers["bearer"].Headers["Authorization"]; got != "Bearer $API_KEY" {
		t.Errorf("bearer header = %q", got)
	}
	warnings := strings.Join(m.Warnings(context.Background()), "\n")
	for _, want := range []string{
		"gemini leaves out mcp server args: Gemini would expand the $",
		"gemini leaves out mcp server cmd: Gemini would expand the $",
		"gemini leaves out mcp server dollar: Gemini would expand the $",
		"gemini leaves out mcp server under_line: its name has an underscore",
		"gemini leaves out mcp server url: Gemini would expand the $",
		"gemini mcp server oauth signs in with OAuth: run /mcp auth oauth in Gemini CLI",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings lack %q:\n%s", want, warnings)
		}
	}
	if strings.Contains(warnings, "mcp server ok") || strings.Contains(warnings, "mcp server secret") {
		t.Errorf("warned about a server it carries:\n%s", warnings)
	}
}

func TestGeminiProfilesWithoutAToolsList(t *testing.T) {
	a := config.Agents{
		MCP: map[string]config.MCPServer{"docs": {Command: "docs"}, "wiki": {Command: "wiki"}, "files": {Command: "files"}},
		Profiles: map[string]config.Profile{
			"build": {
				Description: "Build", Edit: "allow", Shell: "ask", Network: boolPtr(false), Sandbox: "workspace-write", Reviewer: "auto",
				MCP: map[string]string{"docs": "allow", "wiki": "ask"},
			},
			"plain": {Description: "Plain"},
		},
	}
	m, _ := newGemini(t, a)
	apply(t, plan(t, m))
	build := readFile(t, filepath.Join(m.geminiExtDir(), "agents", "build.md"))
	if strings.Contains(build, "tools:") {
		t.Errorf("a profile without tools lists some:\n%s", build)
	}
	var got []string
	for _, r := range geminiRules(t, m) {
		if r.Subagent != "build" {
			continue
		}
		tool := r.MCPName
		switch v := r.ToolName.(type) {
		case string:
			if tool == "" {
				tool = v
			}
		case []any:
			var parts []string
			for _, p := range v {
				parts = append(parts, p.(string))
			}
			tool = strings.Join(parts, "+")
		}
		got = append(got, r.Decision+" "+tool)
	}
	want := []string{
		"deny web_fetch+google_web_search",
		"deny files",
		"allow write_file+replace",
		"ask_user run_shell_command",
		"allow docs",
		"ask_user wiki",
	}
	if !slices.Equal(got, want) {
		t.Errorf("build rules = %v\nwant %v", got, want)
	}
	for _, r := range geminiRules(t, m) {
		if r.Subagent == "plain" && r.Decision != "deny" {
			t.Errorf("plain gets %+v", r)
		}
	}
	warnings := strings.Join(m.Warnings(context.Background()), "\n")
	if !strings.Contains(warnings, "gemini profile build can't express: sandbox; reviewer: auto; network: false for shell commands") {
		t.Errorf("warnings = %s", warnings)
	}
	assertConverged(t, m)
}

func TestGeminiMissingCLIFailsOnlyGemini(t *testing.T) {
	a := geminiConfig(t)
	a.Providers = []string{config.ProviderClaude, config.ProviderGemini}
	m, r := newGemini(t, a)
	prepareGemini(t, m)
	r.Missing("gemini")
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gemini is not installed") {
		t.Fatalf("err = %v", err)
	}
	var claude int
	for _, c := range changes {
		if c.Group == config.ProviderGemini || strings.Contains(c.Target, ".gemini") {
			t.Errorf("planned %v although gemini fails", c)
		}
		if c.Group == config.ProviderClaude {
			claude++
		}
	}
	if claude == 0 {
		t.Error("claude's changes were dropped")
	}
}

func TestGeminiBadInstructionsFailOnlyGemini(t *testing.T) {
	a := geminiConfig(t)
	a.Providers = []string{config.ProviderClaude, config.ProviderGemini}
	m, _ := newGemini(t, a)
	prepareGemini(t, m)
	writeFile(t, filepath.Join(m.Paths.Root, "INSTR.md"), "{{ if eq .Provider \"gemini\" }}see @missing/file.md{{ else }}fine{{ end }}\n")
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range changes {
		if c.Group == config.ProviderGemini {
			t.Errorf("planned %v although gemini fails", c)
		}
	}
	if len(changes) == 0 {
		t.Errorf("claude's instructions were dropped; err = %v", err)
	}
}

type refusingLink struct{ *geminiRunner }

func (f *refusingLink) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if len(c.Args) > 1 && c.Args[1] == "link" {
		return runner.Result{ExitCode: 1, Stderr: "link refused"}, nil
	}
	return f.geminiRunner.Run(ctx, c)
}

func TestGeminiApplyFailureStopsOnlyGemini(t *testing.T) {
	a := geminiConfig(t)
	a.Providers = []string{config.ProviderClaude, config.ProviderGemini}
	m, r := newGemini(t, a)
	prepareGemini(t, m)
	changes := plan(t, m)
	m.Runner = &refusingLink{r}
	err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), "link refused") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.claudeBackend().dir, "CLAUDE.md")); err != nil {
		t.Errorf("claude's instructions were not applied: %v", err)
	}
}

func TestGeminiForeignExtensionFailsThePlan(t *testing.T) {
	m, _ := newGemini(t, config.Agents{Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/stop"}}}})
	data, _ := json.Marshal(geminiInstall{Source: "/elsewhere/zakwas", Type: "link"})
	writeFile(t, m.geminiInstallPath(), string(data))
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "gemini extensions uninstall zakwas") {
		t.Errorf("err = %v", err)
	}
	data, _ = json.Marshal(geminiInstall{Source: m.geminiExtDir(), Type: "copy"})
	writeFile(t, m.geminiInstallPath(), string(data))
	if _, err := m.Plan(context.Background()); err == nil {
		t.Error("a copied extension of that name was taken for zakwas's link")
	}
}

func TestGeminiNoLongerManaged(t *testing.T) {
	m, r := newGemini(t, geminiConfig(t))
	prepareGemini(t, m)
	apply(t, plan(t, m))

	m.Agents = config.Agents{Providers: []string{config.ProviderClaude}}
	changes := plan(t, m)
	if len(changes) == 0 || changes[0].Target != "gemini extension zakwas" || changes[0].Action != engine.Remove || !changes[0].Destructive {
		t.Fatalf("changes = %v, want the uninstall first, before gemini's files go", targets(changes))
	}
	apply(t, changes)
	if !r.Ran("gemini extensions uninstall zakwas") {
		t.Errorf("ran %v", r.Lines())
	}
	for _, gone := range []string{
		filepath.Join(m.geminiExtDir(), "gemini-extension.json"), filepath.Join(m.geminiExtDir(), "hooks", "hooks.json"),
		filepath.Join(m.geminiExtDir(), "GEMINI.md"), filepath.Join(m.geminiExtDir(), "agents", "review.md"),
		m.geminiPolicyPath(), filepath.Join(m.Paths.Home, ".agents", "skills", "mine"), m.geminiStatePath(),
	} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", gone, err)
		}
	}
	assertConverged(t, m)
}

func TestGeminiNoLongerManagedWithoutTheCLI(t *testing.T) {
	m, r := newGemini(t, config.Agents{Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/stop"}}}})
	apply(t, plan(t, m))
	m.Agents = config.Agents{Providers: []string{config.ProviderClaude}}
	r.Missing("gemini")
	for _, c := range plan(t, m) {
		if strings.Contains(c.Target, "extension zakwas") {
			t.Errorf("planned %v without gemini installed", c)
		}
	}
}

func TestGeminiEditedFilesAreBackedUp(t *testing.T) {
	m, _ := newGemini(t, config.Agents{Permissions: &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git status"}}}})
	apply(t, plan(t, m))
	path := m.geminiPolicyPath()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "[[rule]]\ntoolName = \"*\"\ndecision = \"allow\"\npriority = 999\n")
	changes := plan(t, m)
	if len(changes) != 1 || !strings.Contains(changes[0].Detail, "back up to zakwas.toml.zakwas-bak") {
		t.Fatalf("changes = %v", targets(changes))
	}
	apply(t, changes)
	if got := readFile(t, path+".zakwas-bak"); !strings.Contains(got, "priority = 999") {
		t.Errorf("backup = %q", got)
	}
	assertConverged(t, m)
}

func TestGeminiHome(t *testing.T) {
	m, r := newGemini(t, geminiConfig(t))
	prepareGemini(t, m)
	m.GeminiHome = filepath.Join(t.TempDir(), "gemini-home")
	apply(t, plan(t, m))
	ext := filepath.Join(m.GeminiHome, ".gemini", "zakwas")
	for _, path := range []string{
		filepath.Join(ext, "gemini-extension.json"), filepath.Join(ext, "GEMINI.md"), filepath.Join(m.GeminiHome, ".gemini", "policies", "zakwas.toml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Error(err)
		}
	}
	assertLink(t, filepath.Join(m.GeminiHome, ".agents", "skills", "mine"), filepath.Join(m.Paths.Root, "skills", "mine"))
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".gemini")); !os.IsNotExist(err) {
		t.Errorf("wrote into the real home: %v", err)
	}
	link := r.Calls[len(r.Calls)-1]
	if len(link.Env) != 1 || link.Env[0] != "GEMINI_CLI_HOME="+m.GeminiHome {
		t.Errorf("link env = %v", link.Env)
	}
	assertConverged(t, m)
}

func TestGeminiMovedHomeUninstallsTheOldExtension(t *testing.T) {
	for _, tt := range []struct {
		name     string
		from, to func(m *Module) string
	}{
		{"unset to a value", func(m *Module) string { return "" }, func(m *Module) string { return filepath.Join(t.TempDir(), "b") }},
		{"a value to another", func(m *Module) string { return filepath.Join(t.TempDir(), "a") }, func(m *Module) string { return filepath.Join(t.TempDir(), "b") }},
		{"a value to unset", func(m *Module) string { return filepath.Join(t.TempDir(), "a") }, func(m *Module) string { return "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, r := newGemini(t, geminiConfig(t))
			prepareGemini(t, m)
			m.GeminiHome = tt.from(m)
			oldBase := m.geminiBase()
			apply(t, plan(t, m))
			if _, err := os.Stat(geminiInstallPathAt(oldBase)); err != nil {
				t.Fatal(err)
			}

			m.GeminiHome = tt.to(m)
			r.Calls = nil
			changes := plan(t, m)
			apply(t, changes)

			var uninstall *runner.Cmd
			for i, c := range r.Calls {
				if len(c.Args) > 1 && c.Args[1] == "uninstall" {
					uninstall = &r.Calls[i]
				}
			}
			if uninstall == nil || len(uninstall.Env) != 1 || uninstall.Env[0] != "GEMINI_CLI_HOME="+oldBase {
				t.Fatalf("uninstall = %+v, want one with GEMINI_CLI_HOME=%s; calls %v", uninstall, oldBase, r.Lines())
			}
			for _, path := range []string{
				filepath.Join(oldBase, ".gemini", "extensions", geminiExtension),
				filepath.Join(oldBase, ".gemini", geminiExtension, "gemini-extension.json"),
			} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Errorf("%s left behind: %v", path, err)
				}
			}
			if _, err := os.Stat(m.geminiInstallPath()); err != nil {
				t.Errorf("the extension is not linked in the new home: %v", err)
			}
			assertConverged(t, m)
		})
	}
}

func TestGeminiMovedHomeLeavesAnExtensionZakwasDidNotLink(t *testing.T) {
	m, r := newGemini(t, geminiConfig(t))
	prepareGemini(t, m)
	m.GeminiHome = filepath.Join(t.TempDir(), "a")
	oldBase := m.GeminiHome
	apply(t, plan(t, m))
	writeFile(t, geminiInstallPathAt(oldBase), `{"source":"https://example.com/x","type":"git"}`)

	m.GeminiHome = filepath.Join(t.TempDir(), "b")
	r.Calls = nil
	apply(t, plan(t, m))
	for _, c := range r.Calls {
		if len(c.Args) > 1 && c.Args[1] == "uninstall" {
			t.Errorf("uninstalled an extension zakwas did not link: %v", r.Lines())
		}
	}
	assertConverged(t, m)
}

func TestGeminiMovedHomeSkipsAnOldExtensionItCannotUninstall(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, old string){
		"manifest gone": func(t *testing.T, old string) {
			if err := os.Remove(filepath.Join(old, ".gemini", geminiExtension, "gemini-extension.json")); err != nil {
				t.Fatal(err)
			}
		},
		"record unreadable": func(t *testing.T, old string) { writeFile(t, geminiInstallPathAt(old), "{not json") },
		"record missing": func(t *testing.T, old string) {
			if err := os.RemoveAll(filepath.Dir(geminiInstallPathAt(old))); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, r := newGemini(t, geminiConfig(t))
			prepareGemini(t, m)
			m.GeminiHome = filepath.Join(t.TempDir(), "a")
			oldBase := m.GeminiHome
			apply(t, plan(t, m))
			damage(t, oldBase)

			m.GeminiHome = filepath.Join(t.TempDir(), "b")
			r.Calls = nil
			apply(t, plan(t, m))
			for _, c := range r.Calls {
				if len(c.Args) > 1 && c.Args[1] == "uninstall" {
					t.Errorf("ran an uninstall for a home it cannot uninstall from: %v", r.Lines())
				}
			}
			if _, err := os.Stat(m.geminiInstallPath()); err != nil {
				t.Errorf("the extension is not linked in the new home: %v", err)
			}
			assertConverged(t, m)
		})
	}
}

func TestGeminiMovedHomeWithGeminiDroppedUninstallsTheOldExtension(t *testing.T) {
	m, r := newGemini(t, geminiConfig(t))
	prepareGemini(t, m)
	m.GeminiHome = filepath.Join(t.TempDir(), "a")
	oldBase := m.GeminiHome
	apply(t, plan(t, m))

	m.GeminiHome = filepath.Join(t.TempDir(), "b")
	m.Agents.Providers = []string{config.ProviderClaude}
	r.Calls = nil
	apply(t, plan(t, m))
	if _, err := os.Lstat(filepath.Dir(geminiInstallPathAt(oldBase))); !os.IsNotExist(err) {
		t.Errorf("old extension record left behind: %v", err)
	}
	assertConverged(t, m)
}

func TestGeminiSharesTheAgentsSkillsLinkWithCodex(t *testing.T) {
	m, _ := newGemini(t, config.Agents{Providers: []string{config.ProviderCodex, config.ProviderGemini}, Skills: []config.Skill{{Path: "skills/mine"}}})
	prepareGemini(t, m)
	changes := plan(t, m)
	var links int
	for _, c := range changes {
		if strings.HasSuffix(c.Target, "/skills/mine") {
			links++
		}
	}
	if links != 1 {
		t.Errorf("planned %d links to ~/.agents/skills/mine, want 1: %v", links, targets(changes))
	}
}

func TestGeminiNotNamedPlansNothing(t *testing.T) {
	a := geminiConfig(t)
	a.Profiles = nil
	a.Skills = nil
	m, r := newGemini(t, a)
	m.Agents.Providers = []string{config.ProviderClaude}
	prepareGemini(t, m)
	for _, c := range plan(t, m) {
		if strings.Contains(strings.ToLower(c.Target), "gemini") {
			t.Errorf("planned %v", c)
		}
	}
	if r.Ran("gemini") {
		t.Errorf("ran %v", r.Lines())
	}
}

func TestGeminiWithoutAnythingForTheExtensionWritesNone(t *testing.T) {
	m, r := newGemini(t, config.Agents{
		Skills:      []config.Skill{{Path: "skills/mine"}},
		Permissions: &config.AgentPermissions{Deny: []config.AgentPermission{{Command: "rm -rf", Justification: `don't "rm" it \ ever`}}},
	})
	prepareGemini(t, m)
	apply(t, plan(t, m))
	if _, err := os.Lstat(m.geminiExtDir()); !os.IsNotExist(err) {
		t.Errorf("wrote an extension: %v", err)
	}
	if r.Ran("gemini") {
		t.Errorf("ran %v", r.Lines())
	}
	rules := geminiRules(t, m)
	if len(rules) != 1 || rules[0].DenyMessage != `don't "rm" it \ ever` {
		t.Errorf("rules = %+v", rules)
	}
	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "mine"), filepath.Join(m.Paths.Root, "skills", "mine"))
	assertConverged(t, m)
}

func TestGeminiUnlinksWhenTheExtensionEmpties(t *testing.T) {
	m, r := newGemini(t, config.Agents{
		Hooks:       &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/stop"}}},
		Permissions: &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git status"}}},
	})
	apply(t, plan(t, m))
	m.Agents.Hooks = nil
	changes := plan(t, m)
	if len(changes) == 0 || changes[0].Target != "gemini extension zakwas" || changes[0].Action != engine.Remove {
		t.Fatalf("changes = %v", targets(changes))
	}
	apply(t, changes)
	if !r.Ran("gemini extensions uninstall zakwas") {
		t.Errorf("ran %v", r.Lines())
	}
	if _, err := os.Stat(m.geminiPolicyPath()); err != nil {
		t.Errorf("the policy file went with the extension: %v", err)
	}
	assertConverged(t, m)
}

func TestGeminiKeepsTheManifestUntilItCanUnlink(t *testing.T) {
	m, r := newGemini(t, config.Agents{Hooks: &config.AgentHooks{Commands: []config.AgentCommandHook{{Event: "stop", Command: "/bin/stop"}}}})
	apply(t, plan(t, m))
	manifest := filepath.Join(m.geminiExtDir(), "gemini-extension.json")

	m.Agents = config.Agents{Providers: []string{config.ProviderClaude}}
	r.Missing("gemini")
	apply(t, plan(t, m))
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("the manifest went before the link could: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.geminiExtDir(), "hooks", "hooks.json")); !os.IsNotExist(err) {
		t.Errorf("hooks.json is still there: %v", err)
	}

	r.Install("gemini")
	changes := plan(t, m)
	if len(changes) == 0 || changes[0].Target != "gemini extension zakwas" {
		t.Fatalf("changes = %v, want the uninstall first", targets(changes))
	}
	apply(t, changes)
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Errorf("the manifest is still there: %v", err)
	}
	if _, err := os.Stat(m.geminiInstallPath()); !os.IsNotExist(err) {
		t.Errorf("the link is still there: %v", err)
	}
	assertConverged(t, m)
}
