package templates

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/install"
)

func setup(t *testing.T, tmpl string, mode os.FileMode) (*Module, string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "k9s.yaml.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Module{
		Templates: config.Templates{
			Vars:  map[string]string{"theme": "nord-light"},
			Files: []config.TemplateFile{{Src: "k9s.yaml.tmpl", Dst: "~/.config/k9s/config.yaml", Mode: mode}},
		},
		Paths: config.Paths{Home: home, Root: root},
	}
	state, err := install.LoadState(install.StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	m.Installer = &install.Installer{Paths: m.Paths, State: state}
	return m, filepath.Join(home, ".config/k9s/config.yaml")
}

func plan(t *testing.T, m *Module) []engine.Change {
	t.Helper()
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func apply(t *testing.T, changes []engine.Change) {
	t.Helper()
	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "templates", Changes: changes}}); err != nil {
		t.Fatal(err)
	}
}

func TestRenderAndConverge(t *testing.T) {
	m, dst := setup(t, "dir: {{.Home}}/dumps\nskin: {{.Vars.theme}}\n", 0o600)

	changes := plan(t, m)
	if len(changes) != 1 || changes[0].Action != engine.Create {
		t.Fatalf("changes = %v", changes)
	}
	apply(t, changes)

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := "dir: " + m.Paths.Home + "/dumps\nskin: nord-light\n"
	if string(got) != want {
		t.Errorf("rendered %q, want %q", got, want)
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o400 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("not idempotent: %v", again)
	}
}

func TestDrift(t *testing.T) {
	tests := []struct {
		name       string
		drift      func(t *testing.T, dst string)
		wantDetail string
	}{
		{"edited in place", func(t *testing.T, dst string) {
			if err := os.Chmod(dst, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, []byte("edited"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "edited in place"},
		{"made writable", func(t *testing.T, dst string) {
			if err := os.Chmod(dst, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "mode 644 → 444"},
		{"home-manager symlink", func(t *testing.T, dst string) {
			if err := os.Remove(dst); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nix/store/x", dst); err != nil {
				t.Fatal(err)
			}
		}, "replace symlink"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, dst := setup(t, "static\n", 0)
			apply(t, plan(t, m))
			tt.drift(t, dst)

			changes := plan(t, m)
			if len(changes) != 1 || !strings.Contains(changes[0].Detail, tt.wantDetail) {
				t.Fatalf("changes = %v, want detail %q", changes, tt.wantDetail)
			}
			apply(t, changes)
			if again := plan(t, m); len(again) != 0 {
				t.Errorf("still drifted: %v", again)
			}
		})
	}
}

func TestUnknownVariableFails(t *testing.T) {
	m, _ := setup(t, "{{.Vars.missing}}", 0)
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %v", err)
	}
}

var permissions = &config.Agents{
	Providers: []string{config.ProviderClaude, config.ProviderOpencode},
	Permissions: &config.AgentPermissions{
		Allow: []config.AgentPermission{
			{Command: "git  status"},
			{Command: "sed -n", Providers: []string{config.ProviderClaude}},
			{Command: "peekaboo see", Providers: []string{config.ProviderOpencode}},
		},
		Review: []config.AgentPermission{{Command: "git push"}},
		Deny:   []config.AgentPermission{{Command: "git push --force", Justification: "use --force-with-lease"}},
	},
}

func TestAgentPermissions(t *testing.T) {
	entries := `"git status": "allow", "git status *": "allow", "peekaboo see": "allow", "peekaboo see *": "allow", ` +
		`"git push": "ask", "git push *": "ask", "git push --force": "deny", "git push --force *": "deny"`
	tests := []struct {
		name    string
		agents  *config.Agents
		args    []string
		want    string
		wantErr string
	}{
		{"catch-all first, deny last", permissions, []string{"opencode", "ask"}, `{"*": "ask", ` + entries + `}`, ""},
		{"without catch-all", permissions, []string{"opencode"}, `{` + entries + `}`, ""},
		{"no agents section", nil, []string{"opencode", "allow"}, `{"*": "allow"}`, ""},
		{"nothing declared", &config.Agents{}, []string{"opencode"}, `{}`, ""},
		{"other provider", permissions, []string{"claude"}, "", `provider "claude" is not supported, only opencode`},
		{"unknown action", permissions, []string{"opencode", "prompt"}, "", `default action "prompt" is not one of allow, ask, deny`},
		{"two actions", permissions, []string{"opencode", "allow", "deny"}, "", "want at most one default action, got 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := agentPermissions(tt.agents, tt.args[0], tt.args[1:]...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("not JSON: %s", got)
			}
		})
	}
}

// The loader rejects quotes and the like in commands; the JSON stays valid
// even when one gets through.
func TestAgentPermissionsEscape(t *testing.T) {
	command := `say "hi" \ <b> & café`
	a := &config.Agents{Permissions: &config.AgentPermissions{Deny: []config.AgentPermission{{Command: command}}}}
	got, err := agentPermissions(a, config.ProviderOpencode)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if want := map[string]string{command: "deny", command + " *": "deny"}; !maps.Equal(decoded, want) {
		t.Errorf("decoded %v, want %v", decoded, want)
	}
}

func TestAgentPermissionsInTemplate(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	tmpl := `{
  "permission": {"bash": {{ agentPermissions "opencode" "ask" }}},
  "agent": {"build": {"permission": {"bash": {{ agentPermissions "opencode" "allow" }}}}}
}
`
	if err := os.WriteFile(filepath.Join(root, "opencode.json.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Module{
		Templates: config.Templates{Files: []config.TemplateFile{{Src: "opencode.json.tmpl", Dst: "~/.config/opencode/opencode.json"}}},
		Agents:    permissions,
		Paths:     config.Paths{Home: home, Root: root},
	}
	m.Installer = &install.Installer{Paths: m.Paths, StatePath: install.StatePath(home)}
	apply(t, plan(t, m))
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Permission struct {
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
		Agent map[string]struct {
			Permission struct {
				Bash map[string]string `json:"bash"`
			} `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("rendered opencode.json is not JSON: %v\n%s", err, data)
	}
	if got := cfg.Permission.Bash["*"]; got != "ask" {
		t.Errorf("global catch-all = %q", got)
	}
	if got := cfg.Agent["build"].Permission.Bash["git push --force *"]; got != "deny" {
		t.Errorf("build agent keeps deny = %q", got)
	}
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("not idempotent: %v", again)
	}
}

func TestAgentMCP(t *testing.T) {
	agents := &config.Agents{MCP: map[string]config.MCPServer{
		"zeta":  {Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"LANG": "C"}, EnvVars: []string{"TOKEN"}},
		"alpha": {URL: "https://e.com/mcp?a=1&b=2", BearerTokenEnv: "API_TOKEN", Headers: map[string]string{"X-Org": "acme"}, HeaderEnv: map[string]string{"X-Key": "KEY"}},
		"oauth": {URL: "https://o.com/mcp", OAuth: true},
		"other": {Command: "other", Providers: []string{config.ProviderClaude}},
	}}
	got, err := agentMCP(agents, config.ProviderOpencode)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"alpha": {"type":"remote","url":"https://e.com/mcp?a=1&b=2","headers":{"Authorization":"Bearer {env:API_TOKEN}","X-Key":"{env:KEY}","X-Org":"acme"},"oauth":false,"enabled":true}, ` +
		`"oauth": {"type":"remote","url":"https://o.com/mcp","enabled":true}, ` +
		`"zeta": {"type":"local","command":["npx","-y","pkg"],"environment":{"LANG":"C","TOKEN":"{env:TOKEN}"},"enabled":true}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if !json.Valid([]byte(got)) {
		t.Errorf("not JSON: %s", got)
	}
}

func TestAgentMCPEdgeCases(t *testing.T) {
	if got, err := agentMCP(nil, config.ProviderOpencode); err != nil || got != "{}" {
		t.Errorf("no agents = %q, %v", got, err)
	}
	if _, err := agentMCP(nil, config.ProviderClaude); err == nil || !strings.Contains(err.Error(), `agentMCP: provider "claude" is not supported, only opencode`) {
		t.Errorf("claude: %v", err)
	}
}

func TestAgentMCPInTemplate(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	tmpl := `{"mcp": {{ agentMCP "opencode" }}}
`
	if err := os.WriteFile(filepath.Join(root, "opencode.json.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Module{
		Templates: config.Templates{Files: []config.TemplateFile{{Src: "opencode.json.tmpl", Dst: "~/.config/opencode/opencode.json"}}},
		Agents:    &config.Agents{MCP: map[string]config.MCPServer{"docs": {Command: "npx"}}},
		Paths:     config.Paths{Home: home, Root: root},
	}
	m.Installer = &install.Installer{Paths: m.Paths, StatePath: install.StatePath(home)}
	apply(t, plan(t, m))
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"mcp": {"docs": {"type":"local","command":["npx"],"enabled":true}}}` + "\n"; string(data) != want {
		t.Errorf("rendered = %q, want %q", data, want)
	}
}
