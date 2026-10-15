package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func guardedModule(t *testing.T, providers ...string) *Module {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "dotfiles")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := permissionsModule(t, providers, nil)
	m.Paths = config.Paths{Home: home, Root: root}
	m.Guard = true
	return m
}

func settingsDeny(t *testing.T, path string) []string {
	t.Helper()
	var s struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &s); err != nil {
		t.Fatal(err)
	}
	return s.Permissions.Deny
}

func TestGuardDeniesApplyAndEditsInClaude(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{"permissions": {"deny": ["Bash(rm:*)"], "allow": ["Bash(ls:*)"]}}`)

	apply(t, plan(t, m))
	deny := settingsDeny(t, settings)
	for _, want := range []string{
		"Bash(rm:*)",
		"Bash(zakwas apply)", "Bash(zakwas apply *)", "Bash(zakwas * apply)", "Bash(zakwas * apply *)",
		"Bash(zakwas upgrade *)", "Bash(zakwas * upgrade *)",
		"Bash(zakwas self-update)", "Bash(zakwas * self-update *)",
		"Bash(zakwas mcp)", "Bash(zakwas mcp *)", "Bash(*/zakwas * mcp *)",
		"Bash(*/zakwas apply *)", "Bash(*/zakwas * upgrade)", "Bash(*/zakwas * self-update *)",
		"Edit(~/dotfiles/**)", "Edit(~/.local/state/zakwas/**)",
	} {
		if !slices.Contains(deny, want) {
			t.Errorf("deny lacks %q: %q", want, deny)
		}
	}
	for _, plain := range []string{"plan", "check"} {
		if slices.ContainsFunc(deny, func(r string) bool { return strings.Contains(r, "zakwas "+plain) || strings.Contains(r, "* "+plain) }) {
			t.Errorf("deny rules reach %s: %q", plain, deny)
		}
	}
	assertConverged(t, m)

	m.Guard = false
	changes := plan(t, m)
	if len(changes) != 1 || !strings.Contains(changes[0].Summary(), "-42 deny") {
		t.Fatalf("plan = %q", targets(changes))
	}
	apply(t, changes)
	if got := settingsDeny(t, settings); !slices.Equal(got, []string{"Bash(rm:*)"}) {
		t.Errorf("deny after turning the guard off = %q, want the user's rule only", got)
	}
	assertConverged(t, m)
}

func TestGuardKeepsUsersOwnCopyOfARule(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{"permissions": {"deny": ["Bash(zakwas apply)"]}}`)
	apply(t, plan(t, m))
	assertConverged(t, m)

	m.Guard = false
	apply(t, plan(t, m))
	if got := settingsDeny(t, settings); !slices.Equal(got, []string{"Bash(zakwas apply)"}) {
		t.Errorf("deny = %q, want the rule the user already had", got)
	}
}

func TestGuardDeniesTheMainCheckoutOfAWorktree(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	main := m.Paths.Root
	worktree := filepath.Join(m.Paths.Home, "wt", "dotfiles")
	gitdir := filepath.Join(main, ".git", "worktrees", "wt")
	writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitdir+"\n")
	m.Paths.Root = filepath.Join(worktree, "config")
	if err := os.MkdirAll(m.Paths.Root, 0o755); err != nil {
		t.Fatal(err)
	}

	apply(t, plan(t, m))
	deny := settingsDeny(t, filepath.Join(m.Paths.Home, ".claude", "settings.json"))
	for _, want := range []string{"Edit(~/dotfiles/**)", "Edit(~/wt/dotfiles/**)"} {
		if !slices.Contains(deny, want) {
			t.Errorf("deny lacks %q: %q", want, deny)
		}
	}
	assertConverged(t, m)
}

func TestGuardOutsideGitDeniesTheConfigDir(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	m.Paths.Root = t.TempDir()
	apply(t, plan(t, m))
	deny := settingsDeny(t, filepath.Join(m.Paths.Home, ".claude", "settings.json"))
	want := "Edit(/" + filepath.ToSlash(realPath(m.Paths.Root)) + "/**)"
	if !slices.Contains(deny, want) {
		t.Errorf("deny lacks %q: %q", want, deny)
	}
}

func TestGuardCantDenyARepoHoldingHome(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	if err := os.Mkdir(filepath.Join(m.Paths.Home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.Paths.Root = m.Paths.Home
	apply(t, plan(t, m))
	deny := settingsDeny(t, filepath.Join(m.Paths.Home, ".claude", "settings.json"))
	if slices.Contains(deny, "Edit(~/**)") || !slices.Contains(deny, "Edit(~/.local/state/zakwas/**)") {
		t.Errorf("deny = %q", deny)
	}
	warnings := m.Warnings(context.Background())
	if len(warnings) != 1 || !strings.Contains(warnings[0], "claude") || !strings.Contains(warnings[0], "home directory") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestGuardForbidsApplyInCodexOnlyWhenNamed(t *testing.T) {
	named := guardedModule(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, named))
	rules := readFile(t, named.codexRulesPath())
	for _, c := range config.GuardedCommands {
		want := `prefix_rule(pattern=["zakwas", "` + c + `"], decision="forbidden", justification=`
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
	assertConverged(t, named)

	named.Guard = false
	apply(t, plan(t, named))
	if _, err := os.Stat(named.codexRulesPath()); !os.IsNotExist(err) {
		t.Errorf("rules file left behind: %v", err)
	}

	unnamed := guardedModule(t)
	unnamed.Agents.Providers = nil
	apply(t, plan(t, unnamed))
	if _, err := os.Stat(unnamed.codexRulesPath()); !os.IsNotExist(err) {
		t.Errorf("codex got rules without being named: %v", err)
	}
}

func TestGuardRulesAreNotDuplicatedInCodex(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude, config.ProviderCodex)
	m.Agents.Permissions = &config.AgentPermissions{Deny: entries("zakwas apply")}
	apply(t, plan(t, m))
	if got := strings.Count(readFile(t, m.codexRulesPath()), `["zakwas", "apply"]`); got != 1 {
		t.Errorf("zakwas apply is forbidden %d times", got)
	}
}

func TestGuardOffLeavesTheAgentsAlone(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode)
	m.Guard = false
	if changes := plan(t, m); len(changes) != 0 {
		t.Errorf("plan = %q", targets(changes))
	}
	if warnings := m.Warnings(context.Background()); len(warnings) != 0 {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestGuardWarnsAboutOpencodeWithoutAnAgentPermissionsTemplate(t *testing.T) {
	tests := []struct {
		name      string
		providers []string
		template  string
		want      int
	}{
		{"no template", []string{config.ProviderOpencode}, "", 1},
		{"template without the function", []string{config.ProviderOpencode}, `{"permission": {}}`, 1},
		{"template for another provider", []string{config.ProviderOpencode}, `{{ agentPermissions "claude" }}`, 1},
		{"template with the function", []string{config.ProviderOpencode}, `{"bash": {{ agentPermissions "opencode" "ask" }}}`, 0},
		{"opencode not managed", []string{config.ProviderClaude}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := guardedModule(t, tt.providers...)
			if tt.template != "" {
				src := filepath.Join(m.Paths.Root, "opencode.json.tmpl")
				writeFile(t, src, tt.template)
				m.TemplateSources = []string{src}
			}
			got := m.Warnings(context.Background())
			if len(got) != tt.want {
				t.Fatalf("warnings = %q, want %d", got, tt.want)
			}
			if tt.want == 1 && !strings.Contains(got[0], "opencode can't be protected") {
				t.Errorf("warning = %q", got[0])
			}
		})
	}
}

func TestGuardCantDenyAConfigAtHomeWithoutARepo(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	m.Paths.Root = m.Paths.Home
	if err := os.RemoveAll(filepath.Join(m.Paths.Home, "dotfiles")); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))
	deny := settingsDeny(t, filepath.Join(m.Paths.Home, ".claude", "settings.json"))
	for _, rule := range deny {
		if strings.HasPrefix(rule, "Edit(") && rule != "Edit(~/.local/state/zakwas/**)" {
			t.Errorf("deny holds %q, which covers the home directory", rule)
		}
	}
	if warnings := m.Warnings(context.Background()); len(warnings) != 1 || !strings.Contains(warnings[0], "home directory") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestGuardWarnsWhenTheMainCheckoutHoldsHome(t *testing.T) {
	m := guardedModule(t, config.ProviderClaude)
	home := m.Paths.Home
	if err := os.Mkdir(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "dotfiles")); err != nil {
		t.Fatal(err)
	}
	gitdir := filepath.Join(home, ".git", "worktrees", "wt")
	worktree := filepath.Join(t.TempDir(), "wt")
	writeFile(t, filepath.Join(gitdir, "commondir"), "../..\n")
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+gitdir+"\n")
	m.Paths.Root = worktree
	dirs, warning := m.guardedDirs()
	if len(dirs) != 1 || dirs[0] != realPath(worktree) || !strings.Contains(warning, "home directory") {
		t.Errorf("dirs = %q, warning = %q", dirs, warning)
	}
}

func TestOpencodeIsToldToPromptByHand(t *testing.T) {
	servers := map[string]config.MCPServer{
		"zakwas": stdioServer("zakwas", "mcp", "--enable-apply"),
		"plain":  stdioServer("zakwas", "mcp"),
	}
	m := mcpModule(t, []string{config.ProviderOpencode}, servers)
	want := `opencode won't ask before zakwas_apply of MCP server zakwas: add "zakwas_zakwas_apply": "ask" to the permission object of its opencode.json template`
	if got := m.Warnings(context.Background()); !slices.Equal(got, []string{want}) {
		t.Errorf("warnings = %q, want %q", got, want)
	}

	tmpl := filepath.Join(t.TempDir(), "opencode.json.tmpl")
	writeFile(t, tmpl, `{"permission": {"zakwas_zakwas_apply": "ask"}}`)
	m.TemplateSources = []string{tmpl}
	if got := m.Warnings(context.Background()); len(got) != 0 {
		t.Errorf("warnings with the permission in the template = %q", got)
	}

	writeFile(t, tmpl, `{"permission": {"zakwas_zakwas_apply": "allow"}}`)
	if got := m.Warnings(context.Background()); !slices.Equal(got, []string{want}) {
		t.Errorf("warnings with the tool allowed in the template = %q, want %q", got, want)
	}

	m.TemplateSources = nil
	m.Agents.Providers = []string{config.ProviderClaude}
	if got := m.Warnings(context.Background()); len(got) != 0 {
		t.Errorf("warnings for a server opencode doesn't get = %q", got)
	}
}
