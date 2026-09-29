//go:build integration

package mise

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
)

// TestRepoPinWinsOverInstalledConfig reproduces a Renovate bump: the repo
// config pins a new version while ~/.config/mise/config.toml, not yet
// rewritten by the files module, still pins the old one. The plan must ask
// for the repo's version.
func TestRepoPinWinsOverInstalledConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not on PATH")
	}
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for path, body := range map[string]string{
		filepath.Join(home, ".config/mise/config.toml"): "[tools]\njq = \"1.8.2\"\n",
		filepath.Join(root, "mise.toml"):                "[tools]\njq = \"1.6\"\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Module{
		Mise:   config.Mise{Config: "mise.toml"},
		Paths:  config.Paths{Home: home, Root: root},
		Runner: runner.NewExec(),
	}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, c := range changes {
		targets = append(targets, c.Target)
	}
	if len(targets) != 2 || targets[0] != "jq@1.6" || targets[1] != "mise install" {
		t.Errorf("changes = %v, want [jq@1.6 mise install]", targets)
	}
}

// TestPrunePlansUnreferencedVersion fakes two installed jq versions in an
// isolated mise data dir; the one no config pins must be planned for removal.
func TestPrunePlansUnreferencedVersion(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not on PATH")
	}
	home, root, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MISE_DATA_DIR", data)
	t.Setenv("MISE_STATE_DIR", t.TempDir())
	t.Setenv("MISE_CACHE_DIR", t.TempDir())
	t.Setenv("MISE_OFFLINE", "1")
	pins := "[tools]\njq = \"1.8.2\"\n"
	for path, body := range map[string]string{
		filepath.Join(home, ".config/mise/config.toml"):       pins,
		filepath.Join(root, "mise.toml"):                      pins,
		filepath.Join(data, "installs/jq/.mise.backend.toml"): "short = \"jq\"\nfull = \"aqua:jqlang/jq\"\n",
		filepath.Join(data, "installs/jq/1.7.1/bin/.keep"):    "",
		filepath.Join(data, "installs/jq/1.8.2/bin/.keep"):    "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Module{
		Mise:   config.Mise{Config: "mise.toml", Prune: true},
		Paths:  config.Paths{Home: home, Root: root},
		Runner: runner.NewExec(),
	}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, c := range changes {
		targets = append(targets, c.Target)
	}
	if len(targets) != 2 || targets[0] != "jq@1.7.1" || targets[1] != "mise prune" {
		t.Errorf("changes = %v, want [jq@1.7.1 mise prune]", targets)
	}
}
