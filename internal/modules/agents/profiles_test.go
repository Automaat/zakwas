package agents

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
)

func boolPtr(b bool) *bool { return &b }

// profileFiles keeps the changes that write a profile file.
func profileFiles(changes []engine.Change) []engine.Change {
	var out []engine.Change
	for _, c := range changes {
		if strings.HasSuffix(c.Target, ".md") || strings.HasSuffix(c.Target, ".config.toml") {
			out = append(out, c)
		}
	}
	return out
}

func profileModule(t *testing.T, a config.Agents) *Module {
	t.Helper()
	m, _ := newModule(t, a)
	return m
}

func reviewProfile() config.Profile {
	return config.Profile{
		Description: "Read-only review",
		Prompt:      "review.md",
		Tools:       []string{"read", "search", "shell"},
		Edit:        config.PermissionDeny,
		Shell:       "ask",
		Network:     boolPtr(false),
		Reviewer:    "auto",
		MCP:         map[string]string{"datadog": "allow", "chrome": "ask"},
		Claude:      &config.ProfileProvider{Model: "sonnet", Overrides: map[string]any{"color": "red"}},
		Codex:       &config.ProfileProvider{Model: "gpt-5", Overrides: map[string]any{"model_reasoning_effort": "high"}},
		Opencode:    &config.ProfileProvider{Model: "anthropic/claude-sonnet-4"},
	}
}

func profileAgents() config.Agents {
	return config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode},
		MCP: map[string]config.MCPServer{
			"datadog": {URL: "https://dd.example.com/mcp"},
			"chrome":  {Command: "chrome-mcp"},
		},
		Permissions: &config.AgentPermissions{
			Allow: []config.AgentPermission{{Command: "git status"}},
			Deny:  []config.AgentPermission{{Command: "git push"}},
		},
		Profiles: map[string]config.Profile{
			"review": reviewProfile(),
			"build":  {Description: "Build", Edit: config.PermissionAllow, Shell: config.PermissionAllow, Sandbox: "workspace-write"},
		},
	}
}

func TestProfilesRenderEveryProvider(t *testing.T) {
	m := profileModule(t, profileAgents())
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude-dir")
	m.CodexHome = filepath.Join(m.Paths.Home, "codex-home")
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "Review as {{ .Provider }}.\nSecond line.\n")

	changes := profileFiles(plan(t, m))
	want := []string{
		"claude: + ~/claude-dir/agents/zakwas/build.md",
		"claude: + ~/claude-dir/agents/zakwas/review.md",
		"codex: + ~/codex-home/build.config.toml",
		"codex: + ~/codex-home/review.config.toml",
		"opencode: + ~/.config/opencode/agents/build.md",
		"opencode: + ~/.config/opencode/agents/review.md",
	}
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	assertGone(t, filepath.Join(m.Paths.Home, "claude-dir"))
	apply(t, plan(t, m))

	assertCopy(t, filepath.Join(m.Paths.Home, "claude-dir", "agents", "zakwas", "review.md"), `---
name: review
description: Read-only review
model: sonnet
tools:
  - Read
  - Glob
  - Grep
  - Bash
  - mcp__chrome
  - mcp__datadog
permissionMode: auto
color: red
---

Review as claude.
Second line.
`)
	assertCopy(t, filepath.Join(m.Paths.Home, "claude-dir", "agents", "zakwas", "build.md"), `---
name: build
description: Build
disallowedTools:
  - mcp__chrome
  - mcp__datadog
permissionMode: acceptEdits
---
`)
	assertCopy(t, filepath.Join(m.Paths.Home, "codex-home", "review.config.toml"), `approval_policy = 'on-request'
approvals_reviewer = 'auto_review'
developer_instructions = "Review as codex.\nSecond line.\n"
model = 'gpt-5'
model_reasoning_effort = 'high'
sandbox_mode = 'read-only'

[mcp_servers]
[mcp_servers.chrome]
default_tools_approval_mode = 'prompt'
enabled = true

[mcp_servers.datadog]
default_tools_approval_mode = 'approve'
enabled = true
`)
	assertCopy(t, filepath.Join(m.Paths.Home, "codex-home", "build.config.toml"), `approval_policy = 'never'
sandbox_mode = 'workspace-write'

[mcp_servers]
[mcp_servers.chrome]
enabled = false

[mcp_servers.datadog]
enabled = false
`)
	review := readFile(t, filepath.Join(m.Paths.Home, ".config", "opencode", "agents", "review.md"))
	for _, want := range []string{
		"mode: primary\n", "model: anthropic/claude-sonnet-4\n",
		"  edit: deny\n  webfetch: deny\n  websearch: deny\n  bash:\n    '*': ask\n    git status: allow\n",
		"    git push: deny\n", "    zakwas apply: deny\n",
		"  chrome_*: ask\n  datadog_*: allow\n---\n\nReview as opencode.\nSecond line.\n",
	} {
		if !strings.Contains(review, want) {
			t.Errorf("opencode review.md lacks %q:\n%s", want, review)
		}
	}
	if strings.Contains(review, "glob:") || strings.Contains(review, "read: deny") {
		t.Errorf("opencode review.md denies a tool the profile keeps:\n%s", review)
	}
	assertConverged(t, m)
}

func TestProfilesReportWhatAProviderCannotExpress(t *testing.T) {
	m := profileModule(t, profileAgents())
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	got := m.Warnings(context.Background())
	want := []string{
		"claude profile build can't express: sandbox; shell: allow",
		"claude profile review can't express: network: false for shell commands; mcp datadog: allow",
		"codex profile review can't express: tools",
		"opencode profile build can't express: sandbox",
		"opencode profile review can't express: reviewer: auto; network: false for shell commands",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("warnings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if strings.Contains(c.Target+c.Detail, "can't express") {
			t.Errorf("a limitation is a plan step: %s %s", c.Target, c.Detail)
		}
	}
}

func TestProfilesCodexIsOptIn(t *testing.T) {
	a := profileAgents()
	m := profileModule(t, a)
	m.Agents.Providers = nil
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	for _, g := range groups(profileFiles(plan(t, m))) {
		if strings.HasPrefix(g, "codex:") {
			t.Errorf("codex profile planned without naming codex: %s", g)
		}
	}
	p := a.Profiles["build"]
	p.Providers = []string{config.ProviderCodex}
	a.Profiles["build"] = p
	m = profileModule(t, a)
	m.Agents.Providers = nil
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	var codex []string
	for _, g := range groups(profileFiles(plan(t, m))) {
		if strings.HasPrefix(g, "codex:") {
			codex = append(codex, g)
		}
	}
	if len(codex) != 1 || !strings.Contains(codex[0], "build.config.toml") {
		t.Errorf("codex changes = %v, want only build", codex)
	}
}

func TestProfilesSkipMCPServersAProviderDoesNotGet(t *testing.T) {
	a := profileAgents()
	chrome := a.MCP["chrome"]
	chrome.Providers = []string{config.ProviderClaude}
	a.MCP["chrome"] = chrome
	m := profileModule(t, a)
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	apply(t, plan(t, m))
	if got := readFile(t, filepath.Join(m.Paths.Home, ".codex", "review.config.toml")); strings.Contains(got, "chrome") {
		t.Errorf("codex profile names a server codex doesn't get:\n%s", got)
	}
}

func TestProfilesFollowChangesAndRemoveOnlyTheirOwn(t *testing.T) {
	m := profileModule(t, profileAgents())
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "v1\n")
	foreign := filepath.Join(m.Paths.Home, ".claude", "agents", "mine.md")
	writeFile(t, foreign, "mine\n")
	apply(t, plan(t, m))

	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "v2\n")
	changes := plan(t, m)
	if len(changes) != 3 {
		t.Fatalf("plan after a prompt change = %v", groups(changes))
	}
	for _, c := range changes {
		if strings.Contains(c.Detail, "back up") {
			t.Errorf("an unedited copy is backed up: %v", c.Detail)
		}
	}
	apply(t, changes)
	assertConverged(t, m)

	delete(m.Agents.Profiles, "build")
	changes = plan(t, m)
	var removed []string
	for _, c := range changes {
		if c.Action != "-" || !c.Destructive {
			t.Errorf("unexpected change %s %s", c.Action, c.Target)
		}
		removed = append(removed, c.Target)
	}
	want := []string{"~/.claude/agents/zakwas/build.md", "~/.codex/build.config.toml", "~/.config/opencode/agents/build.md"}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removals = %v, want %v", removed, want)
	}
	apply(t, changes)
	assertGone(t, filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "build.md"))
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "review.md"), readFile(t, filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "review.md")))
	assertConverged(t, m)

	m.Agents.Profiles = nil
	apply(t, plan(t, m))
	assertGone(t, filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "review.md"))
	assertGone(t, m.profilesStatePath())
	if got := readFile(t, foreign); got != "mine\n" {
		t.Errorf("a file zakwas didn't render changed: %q", got)
	}
	assertConverged(t, m)
}

func TestProfilesBackUpEditedFileOnRemoval(t *testing.T) {
	m := profileModule(t, config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d"}}})
	apply(t, plan(t, m))
	dst := filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "p.md")
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, "edited\n")
	m.Agents.Profiles = nil
	changes := plan(t, m)
	if len(changes) != 1 || !strings.Contains(changes[0].Detail, "back up to p.md.zakwas-bak") {
		t.Fatalf("plan = %v", targets(changes))
	}
	apply(t, changes)
	if got := readFile(t, dst+".zakwas-bak"); got != "edited\n" {
		t.Errorf("backup = %q", got)
	}
}

func TestProfilesFailAnOpencodeTemplateName(t *testing.T) {
	m := profileModule(t, profileAgents())
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	writeFile(t, filepath.Join(m.Paths.Root, "opencode.json.tmpl"), `{"agent": {"review": {"description": "mine"}}, "x": "{{ .Home }}"}`)
	m.TemplateFiles = []config.TemplateFile{{Src: "opencode.json.tmpl", Dst: "~/.config/opencode/opencode.json"}}
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), `profile "review" is also defined by the opencode template`) {
		t.Fatalf("err = %v", err)
	}
	for _, c := range changes {
		if c.Group == config.ProviderOpencode {
			t.Errorf("planned %s despite the conflict", c.Target)
		}
	}
	var claude int
	for _, c := range profileFiles(changes) {
		if c.Group == config.ProviderClaude {
			claude++
		}
	}
	if claude != 2 {
		t.Errorf("claude changes = %d, want the other providers to still apply", claude)
	}
}

func TestProfilesPromptErrors(t *testing.T) {
	m := profileModule(t, config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d", Prompt: "nope.md"}}})
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: profiles: profile \"p\": prompt ") {
		t.Fatalf("err = %v", err)
	}
	writeFile(t, filepath.Join(m.Paths.Root, "nope.md"), "{{ .Missing }}")
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "nope.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfilesOpencodeBestEffort(t *testing.T) {
	a := profileAgents()
	a.Profiles = map[string]config.Profile{"p": {Description: "d"}}
	m, fake := newModule(t, a)
	m.Agents.Providers = nil
	fake.Missing("opencode")
	for _, g := range groups(plan(t, m)) {
		if strings.HasPrefix(g, "opencode:") {
			t.Errorf("opencode profile written without opencode: %s", g)
		}
	}
}

func TestProfilesDenyShellAndToolsPerProvider(t *testing.T) {
	m := profileModule(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode},
		Profiles: map[string]config.Profile{"p": {
			Description: "d", Shell: config.PermissionDeny, Edit: "ask", Reviewer: "user", Network: boolPtr(true), Sandbox: "read-only",
		}},
	})
	apply(t, plan(t, m))
	claude := readFile(t, filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "p.md"))
	if !strings.Contains(claude, "disallowedTools:\n  - Bash\n") || !strings.Contains(claude, "permissionMode: default\n") {
		t.Errorf("claude:\n%s", claude)
	}
	codex := readFile(t, filepath.Join(m.Paths.Home, ".codex", "p.config.toml"))
	if !strings.Contains(codex, "approvals_reviewer = 'user'") || !strings.Contains(codex, "sandbox_mode = 'read-only'") || strings.Contains(codex, "approval_policy") {
		t.Errorf("codex:\n%s", codex)
	}
	opencode := readFile(t, filepath.Join(m.Paths.Home, ".config", "opencode", "agents", "p.md"))
	if !strings.Contains(opencode, "  bash: deny\n  edit: ask\n") && !strings.Contains(opencode, "  bash: deny\n") {
		t.Errorf("opencode:\n%s", opencode)
	}
	got := m.Warnings(context.Background())
	want := []string{
		"claude profile p can't express: sandbox",
		"codex profile p can't express: edit: ask; shell: deny; network: true with sandbox read-only",
		"opencode profile p can't express: sandbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("warnings = %v, want %v", got, want)
	}
}

func TestProfilesReadJSONCTemplates(t *testing.T) {
	m := profileModule(t, profileAgents())
	writeFile(t, filepath.Join(m.Paths.Root, "review.md"), "x")
	m.TemplateFiles = []config.TemplateFile{{Src: "opencode.jsonc.tmpl", Dst: "~/.config/opencode/opencode.jsonc"}}
	writeFile(t, filepath.Join(m.Paths.Root, "opencode.jsonc.tmpl"), `{
  // agents of mine
  "url": "http://example.com/*", /* block */
  "agent": {
    "review": {"description": "a // b",},
  },
}`)
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), `profile "review" is also defined by the opencode template`) {
		t.Fatalf("err = %v", err)
	}
	writeFile(t, filepath.Join(m.Paths.Root, "opencode.jsonc.tmpl"), `{"agent": {"review": `)
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "can't read the agents") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfilesKeepFilesWhenOpencodeGoesMissing(t *testing.T) {
	a := config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d"}}}
	m, fake := newModule(t, a)
	m.Agents.Providers = nil
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".config", "opencode", "agents", "p.md"), readFile(t, filepath.Join(m.Paths.Home, ".config", "opencode", "agents", "p.md")))
	fake.Missing("opencode")
	assertConverged(t, m)
}

func TestProfilesClaudeAcceptEditsHonorsNoShellAsk(t *testing.T) {
	m := profileModule(t, config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d", Edit: config.PermissionAllow, Shell: "ask"}}})
	got := m.Warnings(context.Background())
	want := []string{"claude profile p can't express: shell: ask with edit: allow (acceptEdits also approves filesystem commands)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("warnings = %v, want %v", got, want)
	}
}

func TestAgentFilesRejectSharedDestinations(t *testing.T) {
	tests := []struct {
		name         string
		agents       func() config.Agents
		destinations []config.Destination
		want         string
	}{
		{
			name: "profile file also a files entry",
			agents: func() config.Agents {
				return config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d"}}}
			},
			destinations: []config.Destination{
				{What: "files[0]", Dst: "~/.claude/agents/zakwas/p.md", Path: "{home}/.claude/agents/zakwas/p.md"},
			},
			want: "claude: profiles: profile \"p\": ~/.claude/agents/zakwas/p.md is also written by files[0] (~/.claude/agents/zakwas/p.md)",
		},
		{
			name: "profile file inside a linked directory, ignoring case",
			agents: func() config.Agents {
				return config.Agents{Profiles: map[string]config.Profile{"p": {Description: "d"}}}
			},
			destinations: []config.Destination{
				{What: "links[2]", Dst: "~/.Claude/Agents", Path: "{home}/.Claude/Agents"},
			},
			want: "is also written by links[2] (~/.Claude/Agents)",
		},
		{
			name: "codex profile file also a template",
			agents: func() config.Agents {
				return config.Agents{Providers: []string{config.ProviderCodex}, Profiles: map[string]config.Profile{"p": {Description: "d"}}}
			},
			destinations: []config.Destination{
				{What: "templates.files[0]", Dst: "~/.codex/p.config.toml", Path: "{home}/.codex/p.config.toml"},
			},
			want: "codex: profiles: profile \"p\": ~/.codex/p.config.toml is also written by templates.files[0]",
		},
		{
			name: "instructions file also a files entry",
			agents: func() config.Agents {
				return config.Agents{Instructions: config.InstructionFiles{"AGENTS.md"}}
			},
			destinations: []config.Destination{
				{What: "files[1]", Dst: "~/.claude/CLAUDE.md", Path: "{home}/.claude/CLAUDE.md"},
			},
			want: "claude: instructions: ~/.claude/CLAUDE.md is also written by files[1] (~/.claude/CLAUDE.md)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := profileModule(t, tt.agents())
			writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "x")
			for _, d := range tt.destinations {
				d.Path = strings.ReplaceAll(d.Path, "{home}", m.Paths.Home)
				m.Destinations = append(m.Destinations, d)
			}
			changes, err := m.Plan(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if len(changes) != 0 {
				t.Errorf("planned %v despite the shared destination", groups(changes))
			}
		})
	}
}

func TestAgentFilesAcceptNeighbouringDestinations(t *testing.T) {
	m := profileModule(t, config.Agents{
		Instructions: config.InstructionFiles{"AGENTS.md"},
		Profiles:     map[string]config.Profile{"p": {Description: "d"}},
	})
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "x")
	m.Destinations = []config.Destination{
		{What: "files[0]", Dst: "~/.claude/settings.json", Path: filepath.Join(m.Paths.Home, ".claude", "settings.json")},
		{What: "links[0]", Dst: "~/.claude/agents/zakwas/p.md.bak", Path: filepath.Join(m.Paths.Home, ".claude", "agents", "zakwas", "p.md.bak")},
	}
	apply(t, plan(t, m))
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan = %v, want none", groups(again))
	}
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "x")
}
