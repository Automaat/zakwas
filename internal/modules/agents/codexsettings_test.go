package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func TestCodexSettingsLifecycle(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, nil)
	m.Agents.Codex = &config.CodexSettings{Settings: map[string]any{"agents": map[string]any{"enabled": true, "max_concurrent_threads_per_session": 8}, "model": "example"}}
	path := codexMCP{m}.path()
	changes := plan(t, m)
	if calls := m.Runner.(*runnertest.Fake).Calls; len(calls) != 0 {
		t.Fatalf("plan ran commands: %v", calls)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %v", targets(changes))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("plan wrote config: %v", err)
	}
	apply(t, changes)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	if got := plan(t, m); len(got) != 0 {
		t.Fatalf("not converged: %v", targets(got))
	}
	data, _ := os.ReadFile(path)
	writeFile(t, path, string(data)+"\n# my server\n[mcp_servers.mine]\ncommand = 'mine'\n")
	m.Agents.Codex.Settings["model"] = "updated"
	changes = plan(t, m)
	data, _ = os.ReadFile(path)
	writeFile(t, path, string(data)+"\n# concurrent change\n[ui]\ntheme = 'dark'\n")
	apply(t, changes)
	data, _ = os.ReadFile(path)
	for _, keep := range []string{"# my server\n[mcp_servers.mine]\ncommand = 'mine'", "# concurrent change\n[ui]\ntheme = 'dark'"} {
		if !strings.Contains(string(data), keep) {
			t.Fatalf("lost %q in %s", keep, data)
		}
	}
	if got := plan(t, m); len(got) != 0 {
		t.Fatalf("not converged: %v", targets(got))
	}
	m.Agents.Codex = nil
	if got := plan(t, m); len(got) != 0 {
		t.Fatalf("absent settings changed config: %v", targets(got))
	}
}

func TestCodexSettingsEdits(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		path        []string
		value       any
		want        string
		fail        bool
	}{
		{"comment", "# before\n[agents] # agents\nenabled = false # keep\nother = 1\n", []string{"agents", "enabled"}, true, "# before\n[agents] # agents\nenabled = true # keep\nother = 1\n", false},
		{"no newline", "enabled = false", []string{"enabled"}, true, "enabled = true", false},
		{"CRLF", "[agents]\r\nenabled = false # keep\r\n", []string{"agents", "enabled"}, true, "[agents]\r\nenabled = true # keep\r\n", false},
		{"dotted", "agents.enabled = false\n", []string{"agents", "enabled"}, true, "agents.enabled = true\n", false},
		{"array", "items = [\n 'a', # old\n 'b',\n] # keep\n", []string{"items"}, []any{"c"}, "items = ['c'] # keep\n", false},
		{"quoted", "[\"a.b\"]\n'key' = false\n", []string{"a.b", "key"}, true, "[\"a.b\"]\n'key' = true\n", false},
		{"malformed", "enabled = [", []string{"enabled"}, true, "", true},
		{"inline", "agents = { enabled = false, other = 1 }\n", []string{"agents", "enabled"}, true, "", true},
		{"conflict", "agents = false\n", []string{"agents", "enabled"}, true, "", true},
		{"new table", "# root\nmodel = 'x'\n[ui]\ncolor = true\n", []string{"agents", "enabled"}, true, "# root\nmodel = 'x'\nagents.enabled = true\n[ui]\ncolor = true\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, _, _, err := editCodexSetting([]byte(tt.input), codexSetting{tt.path, tt.value})
			if (err != nil) != tt.fail {
				t.Fatalf("error = %v", err)
			}
			if !tt.fail && string(next) != tt.want {
				t.Fatalf("got %q, want %q", next, tt.want)
			}
		})
	}
}

func TestCodexSettingsSafety(t *testing.T) {
	for _, kind := range []string{"opt-in", "malformed", "parent-symlink", "read-only", "changed-invalid"} {
		t.Run(kind, func(t *testing.T) {
			m := mcpModule(t, []string{config.ProviderCodex}, nil)
			m.Agents.Codex = &config.CodexSettings{Settings: map[string]any{"model": "x"}}
			path := codexMCP{m}.path()
			switch kind {
			case "opt-in":
				m.Agents.Providers = nil
			case "malformed":
				writeFile(t, path, "broken = [")
			case "parent-symlink":
				if err := os.Symlink(t.TempDir(), filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			case "read-only":
				writeFile(t, path, "model = 'old'\n")
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatal(err)
				}
			case "changed-invalid":
				changes := plan(t, m)
				writeFile(t, path, "broken = [")
				if err := changes[0].Apply(context.Background()); err == nil {
					t.Fatal("apply accepted malformed live file")
				}
				data, _ := os.ReadFile(path)
				if string(data) != "broken = [" {
					t.Fatal("apply damaged file")
				}
				return
			}
			if _, err := m.Plan(context.Background()); err == nil {
				t.Fatal("expected safety error")
			}
		})
	}
}

func TestCodexSettingsConcurrentWrite(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, nil)
	m.Agents.Codex = &config.CodexSettings{Settings: map[string]any{"model": "new"}}
	path := codexMCP{m}.path()
	writeFile(t, path, "model = 'old'\n")
	changes := plan(t, m)
	writeBetween(t, "model = 'old'\n# saved during apply\n[ui]\ncolor = true\n")
	apply(t, changes)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# saved during apply\n[ui]\ncolor = true\n") {
		t.Fatalf("lost concurrent edit: %s", data)
	}
	if got := plan(t, m); len(got) != 0 {
		t.Fatalf("not converged: %v", targets(got))
	}
}

func TestCodexSettingsAndMCP(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderCodex}, map[string]config.MCPServer{"docs": {Command: "docs"}})
	m.Agents.Codex = &config.CodexSettings{Settings: map[string]any{"agents": map[string]any{"enabled": true}}}
	changes := plan(t, m)
	apply(t, changes)
	if got := plan(t, m); len(got) != 0 {
		t.Fatalf("not converged: %v", targets(got))
	}
	data, _ := os.ReadFile(codexMCP{m}.path())
	if !strings.Contains(string(data), "[mcp_servers.docs]") || !strings.Contains(string(data), "agents.enabled = true") {
		t.Fatalf("missing config: %s", data)
	}
}
