package agents

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func permissionsModule(t *testing.T, providers []string, p *config.AgentPermissions) *Module {
	t.Helper()
	return &Module{
		Agents: config.Agents{Providers: providers, Permissions: p},
		Paths:  config.Paths{Home: t.TempDir(), Root: t.TempDir()},
		Runner: runnertest.New(),
	}
}

func entries(commands ...string) []config.AgentPermission {
	out := make([]config.AgentPermission, len(commands))
	for i, c := range commands {
		out[i] = config.AgentPermission{Command: c}
	}
	return out
}

func TestClaudePermissionsKeepUserRules(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{
		Allow:  entries("git status", "sed  -n"),
		Review: entries("git push"),
		Deny:   entries("git push --force"),
	})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{"theme": "dark", "permissions": {"defaultMode": "auto", "allow": ["Bash(ls:*)", "Bash(git status:*)"], "ask": ["Bash(rm:*)"]}, "model": "opus"}`)

	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ ~/.claude/settings.json (permissions +1 allow, +1 deny)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if want := "+allow Bash(sed -n:*)\n+deny Bash(git push --force:*)\n"; changes[0].Diff != want {
		t.Errorf("diff = %q, want %q", changes[0].Diff, want)
	}
	apply(t, changes)
	want := `{
  "theme": "dark",
  "permissions": {
    "defaultMode": "auto",
    "allow": [
      "Bash(ls:*)",
      "Bash(git status:*)",
      "Bash(sed -n:*)"
    ],
    "ask": [
      "Bash(rm:*)"
    ],
    "deny": [
      "Bash(git push --force:*)"
    ]
  },
  "model": "opus"
}
`
	if got := readFile(t, settings); got != want {
		t.Errorf("settings =\n%s\nwant\n%s", got, want)
	}
	assertConverged(t, m)

	m.Agents.Permissions = nil
	changes = plan(t, m)
	if got, want := targets(changes), []string{"~ ~/.claude/settings.json (permissions -1 allow, -1 deny)"}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	apply(t, changes)
	want = `{
  "theme": "dark",
  "permissions": {
    "defaultMode": "auto",
    "allow": [
      "Bash(ls:*)",
      "Bash(git status:*)"
    ],
    "ask": [
      "Bash(rm:*)"
    ]
  },
  "model": "opus"
}
`
	if got := readFile(t, settings); got != want {
		t.Errorf("settings after removal =\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(m.permissionsStatePath()); !os.IsNotExist(err) {
		t.Errorf("state file left behind: %v", err)
	}
	assertConverged(t, m)
}

func TestClaudePermissionsCreateAndDropSettings(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	changes := plan(t, m)
	if got, want := targets(changes), []string{"+ ~/.claude/settings.json (permissions +1 allow)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got, want := readFile(t, settings), "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(git status:*)\"\n    ]\n  }\n}\n"; got != want {
		t.Errorf("settings = %q, want %q", got, want)
	}
	if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %v, %v", info, err)
	}
	assertConverged(t, m)

	m.Agents.Permissions = &config.AgentPermissions{}
	apply(t, plan(t, m))
	if got := readFile(t, settings); got != "{}\n" {
		t.Errorf("settings after removal = %q", got)
	}
	assertConverged(t, m)
}

func TestClaudePermissionsRemovedByHand(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status", "git diff")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	apply(t, plan(t, m))
	writeFile(t, settings, `{"permissions": {"allow": ["Bash(git diff:*)"]}}`)

	m.Agents.Permissions = &config.AgentPermissions{Allow: entries("git diff")}
	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ ~/.claude/settings.json (track permission rules)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	assertConverged(t, m)

	writeFile(t, settings, `{"permissions": {"allow": ["Bash(git diff:*)", "Bash(git diff:*)"]}}`)
	m.Agents.Permissions = nil
	apply(t, plan(t, m))
	if got := readFile(t, settings); !strings.Contains(got, `"Bash(git diff:*)"`) {
		t.Errorf("user's copy of the rule removed: %s", got)
	}
	assertConverged(t, m)
}

func TestClaudePermissionsReadOnlySettings(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{}`)
	if err := os.Chmod(settings, 0o400); err != nil {
		t.Fatal(err)
	}
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: permissions: ~/.claude/settings.json is read-only") {
		t.Fatalf("plan error = %v", err)
	}
}

func TestClaudePermissionsFollowConfigDir(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	old := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	apply(t, plan(t, m))

	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude")
	changes := plan(t, m)
	if got, want := targets(changes), []string{
		"~ ~/.claude/settings.json (permissions -1 allow)",
		"+ ~/claude/settings.json (permissions +1 allow)",
	}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got := readFile(t, old); got != "{}\n" {
		t.Errorf("old settings = %q", got)
	}
	assertConverged(t, m)
}

func TestCodexRules(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude, config.ProviderCodex}, &config.AgentPermissions{
		Allow:  append(entries("git status"), config.AgentPermission{Command: "peekaboo see", Providers: []string{config.ProviderCodex}}),
		Review: entries("git push"),
		Deny:   entries("git push --force"),
	})
	m.CodexHome = filepath.Join(m.Paths.Home, "codex")
	rules := filepath.Join(m.CodexHome, "rules", "zakwas.rules")
	writeFile(t, filepath.Join(m.CodexHome, "rules", "default.rules"), "prefix_rule(pattern=[\"ls\"], decision=\"allow\")\n")

	changes := plan(t, m)
	want := []string{
		"+ ~/.claude/settings.json (permissions +1 allow, +1 deny)",
		"+ ~/codex/rules/zakwas.rules (permissions +2 allow, +1 prompt, +1 forbidden)",
	}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	content := `# Generated by zakwas from agents.permissions in zakwas.yaml; change it there.
prefix_rule(pattern=["git", "status"], decision="allow")
prefix_rule(pattern=["peekaboo", "see"], decision="allow")
prefix_rule(pattern=["git", "push"], decision="prompt")
prefix_rule(pattern=["git", "push", "--force"], decision="forbidden")
`
	if got := readFile(t, rules); got != content {
		t.Errorf("rules =\n%s\nwant\n%s", got, content)
	}
	assertConverged(t, m)

	m.Agents.Permissions.Review = nil
	changes = plan(t, m)
	if got, want := targets(changes), []string{"~ ~/codex/rules/zakwas.rules (permissions -1 prompt)"}; !slices.Equal(got, want) {
		t.Fatalf("update plan = %q, want %q", got, want)
	}
	apply(t, changes)
	assertConverged(t, m)

	writeFile(t, rules, "prefix_rule(pattern=[\"rm\"], decision=\"allow\")\n")
	changes = plan(t, m)
	if got, want := targets(changes), []string{"~ ~/codex/rules/zakwas.rules (permissions -1 allow, +2 allow, +1 forbidden, back up to zakwas.rules.zakwas-bak)"}; !slices.Equal(got, want) {
		t.Fatalf("edited plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if got := readFile(t, rules+".zakwas-bak"); !strings.Contains(got, `["rm"]`) {
		t.Errorf("backup = %q", got)
	}
	assertConverged(t, m)

	m.Agents.Permissions = &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git status", Providers: []string{config.ProviderClaude}}}}
	changes = plan(t, m)
	if got, want := targets(changes), []string{
		"~ ~/.claude/settings.json (permissions -1 deny)",
		"- ~/codex/rules/zakwas.rules (permissions -2 allow, -1 forbidden)",
	}; !slices.Equal(got, want) {
		t.Fatalf("removal plan = %q, want %q", got, want)
	}
	if !changes[1].Destructive {
		t.Error("removing the rules file is not marked destructive")
	}
	apply(t, changes)
	if _, err := os.Stat(rules); !os.IsNotExist(err) {
		t.Errorf("rules file left behind: %v", err)
	}
	if got := readFile(t, filepath.Join(m.CodexHome, "rules", "default.rules")); !strings.Contains(got, `["ls"]`) {
		t.Errorf("default.rules changed: %q", got)
	}
	assertConverged(t, m)
}

func TestCodexRulesOptIn(t *testing.T) {
	m := permissionsModule(t, nil, &config.AgentPermissions{Allow: entries("git status")})
	m.Agents.Providers = nil
	for _, c := range plan(t, m) {
		if strings.Contains(c.Target, "codex") {
			t.Errorf("codex not named, still planned %s", c)
		}
	}
	m.Agents.Permissions.Deny = []config.AgentPermission{{Command: "rm", Providers: []string{config.ProviderCodex}}}
	var got []string
	for _, c := range plan(t, m) {
		if c.Group == config.ProviderCodex {
			got = append(got, c.String())
		}
	}
	if want := []string{"+ ~/.codex/rules/zakwas.rules (permissions +1 forbidden)"}; !slices.Equal(got, want) {
		t.Errorf("codex plan = %q, want %q", got, want)
	}
}

func TestCodexRulesLeaveFilesZakwasDidNotWrite(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderCodex}, &config.AgentPermissions{Allow: entries("git status")})
	rules := filepath.Join(m.Paths.Home, ".codex", "rules", "zakwas.rules")
	apply(t, plan(t, m))
	writeFile(t, rules, "# mine\n")

	m.Agents.Permissions = nil
	changes := plan(t, m)
	if got, want := targets(changes), []string{"- ~/.codex/rules/zakwas.rules (no longer zakwas's rules, forget them)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if changes[0].Destructive {
		t.Error("forgetting is marked destructive")
	}
	apply(t, changes)
	if got := readFile(t, rules); got != "# mine\n" {
		t.Errorf("edited rules file changed: %q", got)
	}
	assertConverged(t, m)
}

func TestCodexRulesFollowCodexHome(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderCodex}, &config.AgentPermissions{Allow: entries("git status")})
	old := filepath.Join(m.Paths.Home, ".codex", "rules", "zakwas.rules")
	apply(t, plan(t, m))

	m.CodexHome = filepath.Join(m.Paths.Home, "codex")
	changes := plan(t, m)
	if got, want := targets(changes), []string{
		"- ~/.codex/rules/zakwas.rules (permissions -1 allow)",
		"+ ~/codex/rules/zakwas.rules (permissions +1 allow)",
	}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old rules file left behind: %v", err)
	}
	assertConverged(t, m)
}

func TestRenderCodexRules(t *testing.T) {
	rule := regexp.MustCompile(`^prefix_rule\(pattern=\[("(?:[^"\\]|\\.)*"(?:, )?)+\], decision="(allow|prompt|forbidden)"(?:, justification="(?:[^"\\]|\\.)*")?\)$`)
	data, err := renderCodexRules([]config.Permission{
		{Decision: config.PermissionAllow, Words: []string{"git", "status"}},
		{Decision: config.PermissionReview, Words: []string{"say", `"hi"`, `a\b`}},
		{Decision: config.PermissionDeny, Words: []string{"café"}, Justification: `use "tea" \ not café`},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	want := `# Generated by zakwas from agents.permissions in zakwas.yaml; change it there.
prefix_rule(pattern=["git", "status"], decision="allow")
prefix_rule(pattern=["say", "\"hi\"", "a\\b"], decision="prompt")
prefix_rule(pattern=["café"], decision="forbidden", justification="use \"tea\" \\ not café")
`
	if content != want {
		t.Errorf("rules =\n%s\nwant\n%s", content, want)
	}
	for l := range strings.Lines(content) {
		l = strings.TrimSuffix(l, "\n")
		if !strings.HasPrefix(l, "#") && !rule.MatchString(l) {
			t.Errorf("not a prefix_rule: %s", l)
		}
	}
}

func TestRenderCodexRulesRefusesWhatCodexRejects(t *testing.T) {
	for _, p := range []config.Permission{
		{Decision: config.PermissionAllow, Words: []string{"git\x07"}},
		{Decision: config.PermissionAllow, Words: []string{"ls"}, Justification: "two\nlines"},
		{Decision: config.PermissionAllow, Words: []string{"\xff"}},
		{Decision: config.PermissionDeny, Words: []string{"rm"}, Justification: " "},
	} {
		if _, err := renderCodexRules([]config.Permission{p}); err == nil {
			t.Errorf("rendered %q", p)
		}
	}
}

func TestClaudePermissionsSymlinkedSettingsMove(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Deny: entries("git push --force")})
	target := filepath.Join(m.Paths.Home, "dots", "claude", "settings.json")
	writeFile(t, target, `{"model": "opus"}`)
	if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(m.Paths.Home, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))

	m.ClaudeConfigDir = filepath.Dir(target)
	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ ~/dots/claude/settings.json (track permission rules)"}; !slices.Equal(got, want) {
		t.Fatalf("plan after the move = %q, want %q", got, want)
	}
	apply(t, changes)
	assertConverged(t, m)
	if got := readFile(t, target); !strings.Contains(got, "Bash(git push --force:*)") {
		t.Fatalf("rule lost in the move: %s", got)
	}

	m.Agents.Permissions = nil
	apply(t, plan(t, m))
	if got := readFile(t, target); got != "{\n  \"model\": \"opus\"\n}\n" {
		t.Errorf("rule zakwas added not removed after the move: %s", got)
	}
}

// writeBetween makes beforeReplace write contents, one per call, as if
// Claude Code saved its settings while zakwas edits them.
func writeBetween(t *testing.T, contents ...string) {
	t.Helper()
	calls := 0
	beforeReplace = func(path string) {
		if calls < len(contents) {
			writeFile(t, path, contents[calls])
		}
		calls++
	}
	t.Cleanup(func() { beforeReplace = func(string) {} })
}

func TestClaudePermissionsKeepConcurrentChange(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{"model": "opus"}`)
	changes := plan(t, m)
	writeBetween(t, `{"model": "sonnet", "theme": "dark"}`)
	apply(t, changes)
	want := "{\n  \"model\": \"sonnet\",\n  \"theme\": \"dark\",\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(git status:*)\"\n    ]\n  }\n}\n"
	if got := readFile(t, settings); got != want {
		t.Errorf("settings = %s\nwant %s", got, want)
	}
	assertConverged(t, m)
}

func TestClaudePermissionsKeepConcurrentCreate(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	changes := plan(t, m)
	writeBetween(t, `{"theme": "dark"}`)
	apply(t, changes)
	want := "{\n  \"theme\": \"dark\",\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(git status:*)\"\n    ]\n  }\n}\n"
	if got := readFile(t, settings); got != want {
		t.Errorf("settings = %s\nwant %s", got, want)
	}
	assertConverged(t, m)
}

func TestClaudePermissionsGiveUpOnBusySettings(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude}, &config.AgentPermissions{Allow: entries("git status")})
	settings := filepath.Join(m.Paths.Home, ".claude", "settings.json")
	writeFile(t, settings, `{}`)
	changes := plan(t, m)
	writeBetween(t, `{"n": 1}`, `{"n": 2}`, `{"n": 3}`)
	err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), "kept changing while zakwas edited it") {
		t.Fatalf("apply error = %v", err)
	}
	if got := readFile(t, settings); got != `{"n": 3}` {
		t.Errorf("settings = %s, want the agent's last write", got)
	}
	if _, err := os.Stat(m.permissionsStatePath()); !os.IsNotExist(err) {
		t.Errorf("rules never written are recorded as zakwas's: %v", err)
	}
}

func TestCodexRulesMustBeARegularFile(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderCodex}, &config.AgentPermissions{Allow: entries("git status")})
	rules := filepath.Join(m.Paths.Home, ".codex", "rules", "zakwas.rules")
	target := filepath.Join(m.Paths.Home, "rules-target")
	writeFile(t, target, "")
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, rules); err != nil {
		t.Fatal(err)
	}
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex: permissions: ~/.codex/rules/zakwas.rules: not a regular file") {
		t.Fatalf("plan error = %v", err)
	}
}

func TestPermissionsFailOnlyTheirProvider(t *testing.T) {
	m := permissionsModule(t, []string{config.ProviderClaude, config.ProviderCodex}, &config.AgentPermissions{Allow: entries("git status")})
	if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".claude", "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: permissions: ~/.claude/settings.json is not a regular file") {
		t.Fatalf("plan error = %v", err)
	}
	if got, want := targets(changes), []string{"+ ~/.codex/rules/zakwas.rules (permissions +1 allow)"}; !slices.Equal(got, want) {
		t.Errorf("changes = %q, want %q", got, want)
	}
	for _, c := range changes {
		if c.Action == engine.Create && c.Group != config.ProviderCodex {
			t.Errorf("%s in group %q", c, c.Group)
		}
	}
}
