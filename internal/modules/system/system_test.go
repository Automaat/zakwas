package system

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func applyAll(t *testing.T, changes []engine.Change) {
	t.Helper()
	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
}

func TestDirs(t *testing.T) {
	home := t.TempDir()
	existing := filepath.Join(home, "Documents")
	loose := filepath.Join(home, ".ssh")
	for _, d := range []string{existing, loose} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := &Module{Paths: config.Paths{Home: home}, System: config.System{Dirs: []config.Dir{
		{Path: "~/Documents"},
		{Path: "~/Documents/1_Projects"},
		{Path: "~/.ssh", Mode: 0o700},
	}}}

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %v (existing dir without explicit mode must be left alone)", changes)
	}
	applyAll(t, changes)

	if info, err := os.Stat(filepath.Join(home, "Documents/1_Projects")); err != nil || !info.IsDir() {
		t.Errorf("project dir: %v", err)
	}
	if info, _ := os.Stat(loose); info.Mode().Perm() != 0o700 {
		t.Errorf(".ssh mode = %o", info.Mode().Perm())
	}
	if again, _ := m.Plan(context.Background()); len(again) != 0 {
		t.Errorf("not idempotent: %v", again)
	}
}

func TestDirBlockedByFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Module{Paths: config.Paths{Home: home}, System: config.System{Dirs: []config.Dir{{Path: "~/x"}}}}
	if _, err := m.Plan(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestHasTouchID(t *testing.T) {
	tests := map[string]bool{
		"auth       sufficient     pam_tid.so\n":                true,
		"# sudo_local\nauth sufficient pam_tid.so\n":            true,
		"#auth       sufficient     pam_tid.so\n":               false,
		"auth optional /opt/homebrew/lib/pam/pam_reattach.so\n": false,
		"": false,
	}
	for body, want := range tests {
		p := filepath.Join(t.TempDir(), "sudo_local")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := HasTouchID(p); err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", body, got, err, want)
		}
	}
	if got, err := HasTouchID(filepath.Join(t.TempDir(), "missing")); err != nil || got {
		t.Errorf("missing file: %v, %v", got, err)
	}
}

func TestTouchIDAppendsViaSudo(t *testing.T) {
	pam := filepath.Join(t.TempDir(), "sudo_local")
	fake := runnertest.New().OnOK("sudo tee -a "+pam, "")
	m := &Module{Runner: fake, PAMFile: pam, System: config.System{SudoTouchID: true}}

	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, %v", changes, err)
	}
	applyAll(t, changes)
	if got := fake.Calls[0].Stdin; got != pamTouchID+"\n" {
		t.Errorf("stdin = %q", got)
	}
}

func TestSSHKey(t *testing.T) {
	home := t.TempDir()
	key := filepath.Join(home, ".ssh/id_ed25519")
	fake := runnertest.New().OnOK("ssh-keygen -q -t ed25519 -C me@example.com -f "+key+" -N ", "")
	m := &Module{Runner: fake, Paths: config.Paths{Home: home}, System: config.System{
		SSHKey: &config.SSHKey{Path: "~/.ssh/id_ed25519", Comment: "me@example.com"},
	}}

	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, %v", changes, err)
	}
	applyAll(t, changes)

	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if again, _ := m.Plan(context.Background()); len(again) != 0 {
		t.Errorf("existing key must not be regenerated: %v", again)
	}
}

func TestTouchIDReplacesNixSymlink(t *testing.T) {
	dir := t.TempDir()
	pam := filepath.Join(dir, "sudo_local")
	if err := os.Symlink("/etc/static/pam.d/sudo_local", pam); err != nil {
		t.Fatal(err)
	}
	fake := runnertest.New().OnOK("sudo rm -f "+pam, "").OnOK("sudo tee "+pam, "")
	m := &Module{Runner: fake, PAMFile: pam, System: config.System{SudoTouchID: true}}

	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, %v", changes, err)
	}
	applyAll(t, changes)
	if got := fake.Lines(); len(got) != 2 || got[0] != "sudo rm -f "+pam || got[1] != "sudo tee "+pam {
		t.Errorf("ran %v; must remove the symlink, then write a real file (no -a)", got)
	}
	if fake.Calls[1].Stdin != pamTouchID+"\n" {
		t.Errorf("stdin = %q", fake.Calls[1].Stdin)
	}
}
