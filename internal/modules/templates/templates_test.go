package templates

import (
	"context"
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
