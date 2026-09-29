package install

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
)

func newInstaller(t *testing.T, immutable bool) (*Installer, string) {
	t.Helper()
	home := t.TempDir()
	t.Cleanup(func() {
		if err := Unlock(home); err != nil {
			t.Errorf("unlock: %v", err)
		}
	})
	state, err := LoadState(StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	return &Installer{
		Paths:     config.Paths{Home: home},
		State:     state,
		Immutable: immutable,
	}, home
}

func plan(t *testing.T, in *Installer, dst, want string, perm os.FileMode) *engine.Change {
	t.Helper()
	c, err := in.Plan(dst, []byte(want), perm)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func converge(t *testing.T, in *Installer, dst, want string, perm os.FileMode) *engine.Change {
	t.Helper()
	c := plan(t, in, dst, want, perm)
	if c == nil {
		return nil
	}
	if err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again := plan(t, in, dst, want, perm); again != nil {
		t.Fatalf("not idempotent: %s", again)
	}
	return c
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertProtected(t *testing.T, path, body string, perm os.FileMode, immutable bool) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Errorf("content = %q, %v; want %q", got, err, body)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != perm {
		t.Errorf("mode = %o, want %o", info.Mode().Perm(), perm)
	}
	if locked, _ := isImmutable(path); immutableSupported && locked != immutable {
		t.Errorf("immutable = %v, want %v", locked, immutable)
	}
}

func TestReadOnly(t *testing.T) {
	for in, want := range map[os.FileMode]os.FileMode{0o644: 0o444, 0o755: 0o555, 0o600: 0o400, 0o444: 0o444} {
		if got := ReadOnly(in); got != want {
			t.Errorf("ReadOnly(%o) = %o, want %o", in, got, want)
		}
	}
}

func TestCreate(t *testing.T) {
	for _, immutable := range []bool{false, true} {
		in, home := newInstaller(t, immutable)
		dst := filepath.Join(home, ".config/git/config")

		c := converge(t, in, dst, "v1", 0o644)

		if c.Action != engine.Create || c.Target != "~/.config/git/config" {
			t.Errorf("change = %s", c)
		}
		assertProtected(t, dst, "v1", 0o444, immutable)
		if sum, _ := in.State.Get(dst); sum != Sum([]byte("v1")) {
			t.Error("state not recorded")
		}
		reloaded, err := LoadState(StatePath(home))
		if err != nil || reloaded.Files[dst] != Sum([]byte("v1")) {
			t.Errorf("state not persisted: %v %v", reloaded, err)
		}
	}
}

func TestWriteIsBlocked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	in, home := newInstaller(t, true)
	dst := filepath.Join(home, ".zshrc")
	converge(t, in, dst, "v1", 0o644)

	if err := os.WriteFile(dst, []byte("edited"), 0o644); err == nil {
		t.Error("write to a protected file succeeded")
	}
	if immutableSupported {
		if err := os.Chmod(dst, 0o644); err == nil {
			t.Error("chmod of an immutable file succeeded")
		}
		if err := os.Remove(dst); err == nil {
			t.Error("delete of an immutable file succeeded")
		}
	}
}

// edit simulates a user forcing an in-place change past the protection.
func edit(t *testing.T, path, body string) {
	t.Helper()
	if err := unlock(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, body)
}

func TestUpdates(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, in *Installer, dst string)
		wantDetail string
		wantBackup string
	}{
		{
			name:       "repo changed: overwrite without backup",
			setup:      func(t *testing.T, in *Installer, dst string) { converge(t, in, dst, "old", 0o644) },
			wantDetail: "content",
		},
		{
			name: "edited in place: back up the edit",
			setup: func(t *testing.T, in *Installer, dst string) {
				converge(t, in, dst, "old", 0o644)
				edit(t, dst, "my edit")
			},
			wantDetail: "edited in place, back up to .zshrc.zakwas-bak",
			wantBackup: "my edit",
		},
		{
			name:       "pre-existing unmanaged file: back it up",
			setup:      func(t *testing.T, _ *Installer, dst string) { mustWrite(t, dst, "hand written") },
			wantDetail: "not managed yet, back up to .zshrc.zakwas-bak",
			wantBackup: "hand written",
		},
		{
			name: "home-manager symlink: replace",
			setup: func(t *testing.T, _ *Installer, dst string) {
				if err := os.Symlink("/nix/store/x-home-manager-files/.zshrc", dst); err != nil {
					t.Fatal(err)
				}
			},
			wantDetail: "replace symlink",
		},
		{
			name: "protection removed: restore it",
			setup: func(t *testing.T, in *Installer, dst string) {
				converge(t, in, dst, "new", 0o644)
				if err := unlock(dst); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dst, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantDetail: "mode 644 → 444",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, home := newInstaller(t, true)
			dst := filepath.Join(home, ".zshrc")
			tt.setup(t, in, dst)

			c := converge(t, in, dst, "new", 0o644)

			if c == nil || c.Action != engine.Update || !strings.HasPrefix(c.Detail, tt.wantDetail) {
				t.Fatalf("change = %v, want detail %q", c, tt.wantDetail)
			}
			assertProtected(t, dst, "new", 0o444, true)
			backup, err := os.ReadFile(dst + ".zakwas-bak")
			switch {
			case tt.wantBackup == "" && err == nil:
				t.Errorf("unexpected backup %q", backup)
			case tt.wantBackup != "" && string(backup) != tt.wantBackup:
				t.Errorf("backup = %q, %v; want %q", backup, err, tt.wantBackup)
			}
		})
	}
}

func TestImmutableToggle(t *testing.T) {
	if !immutableSupported {
		t.Skip("no immutable flag on this OS")
	}
	in, home := newInstaller(t, true)
	dst := filepath.Join(home, "f")
	converge(t, in, dst, "x", 0o644)

	in.Immutable = false
	c := converge(t, in, dst, "x", 0o644)
	if c == nil || c.Detail != "clear immutable" {
		t.Fatalf("change = %v", c)
	}
	assertProtected(t, dst, "x", 0o444, false)
}

func TestExecutableBitKept(t *testing.T) {
	in, home := newInstaller(t, false)
	dst := filepath.Join(home, ".local/bin/tool")
	converge(t, in, dst, "#!/bin/sh\n", 0o755)
	assertProtected(t, dst, "#!/bin/sh\n", 0o555, false)
}

func TestDirectoryInTheWay(t *testing.T) {
	in, home := newInstaller(t, false)
	dst := filepath.Join(home, "d")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Plan(dst, nil, 0o644); err == nil {
		t.Error("expected error")
	}
}

func TestBackupNeverClobbers(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "f")
	for i, body := range []string{"first", "second", "third"} {
		mustWrite(t, dst, body)
		got, err := Backup(dst)
		if err != nil {
			t.Fatal(err)
		}
		if want := backupName(dst, i); got != want {
			t.Errorf("backup %d = %s, want %s", i, got, want)
		}
	}
	for i, want := range []string{"first", "second", "third"} {
		if got, err := os.ReadFile(backupName(dst, i)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", backupName(dst, i), got, err, want)
		}
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("original still there: %v", err)
	}
}

// Two changes planned against the same free backup name (or a backup
// appearing between plan and apply) must not overwrite each other.
func TestBackupNameChosenAtApply(t *testing.T) {
	in, home := newInstaller(t, true)
	dst := filepath.Join(home, ".zshrc")
	mustWrite(t, dst, "hand written")
	c := plan(t, in, dst, "new", 0o644)
	if !strings.HasSuffix(c.Detail, "back up to .zshrc.zakwas-bak") {
		t.Fatalf("detail = %q", c.Detail)
	}
	mustWrite(t, dst+".zakwas-bak", "earlier backup")

	if err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		dst + ".zakwas-bak":   "earlier backup",
		dst + ".zakwas-bak.1": "hand written",
	} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", filepath.Base(path), got, err, want)
		}
	}
	assertProtected(t, dst, "new", 0o444, true)
}

func TestBackupNameStopsOnLstatError(t *testing.T) {
	dst := filepath.Join(t.TempDir(), strings.Repeat("a", 250))
	mustWrite(t, dst, "x")
	done := make(chan string, 1)
	go func() { done <- BackupName(dst) }()
	select {
	case got := <-done:
		if want := backupName(dst, 0); got != want {
			t.Errorf("BackupName = %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BackupName loops when the backup name is too long")
	}
	if _, err := Backup(dst); err == nil {
		t.Error("Backup of a too-long name: expected error")
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "x" {
		t.Errorf("original = %q, %v", got, err)
	}
}

func TestBackupRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	mustWrite(t, target, "x")
	if err := os.Chmod(target, 0o444); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "f")
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(dst); err == nil {
		t.Fatal("expected error")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("target mode = %o, want 444", info.Mode().Perm())
	}
	if _, err := os.Lstat(backupName(dst, 0)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("backup created: %v", err)
	}
}

func TestSymlinkedParentRefused(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, in *Installer, home, repo string) string
	}{
		{
			name: "directory moved from links to files",
			setup: func(t *testing.T, in *Installer, home, repo string) string {
				mustWrite(t, filepath.Join(repo, "d/f"), "v1")
				link := filepath.Join(home, ".cfg/d")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(repo, "d"), link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "f")
			},
		},
		{
			name: "symlink under home pointing elsewhere",
			setup: func(t *testing.T, in *Installer, home, _ string) string {
				elsewhere := t.TempDir()
				if err := os.Symlink(elsewhere, filepath.Join(home, ".cfg")); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(home, ".cfg/sub/f")
			},
		},
		{
			name: "outside home, resolving into the repo",
			setup: func(t *testing.T, in *Installer, _, repo string) string {
				mustWrite(t, filepath.Join(repo, "d/f"), "v1")
				link := filepath.Join(t.TempDir(), "d")
				if err := os.Symlink(filepath.Join(repo, "d"), link); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(link, "f")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, home := newInstaller(t, true)
			repo, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			in.Paths.Root = repo
			dst := tt.setup(t, in, home, repo)

			_, err = in.Plan(dst, []byte("v1"), 0o644)
			if err == nil || !strings.Contains(err.Error(), "is a symlink; remove it") {
				t.Fatalf("err = %v", err)
			}
			if info, err := os.Stat(filepath.Join(repo, "d/f")); err == nil && info.Mode().Perm() != 0o644 {
				t.Errorf("repo source mode changed to %o", info.Mode().Perm())
			}
		})
	}
}

func TestSymlinkOutsideHomeAllowed(t *testing.T) {
	in, _ := newInstaller(t, false)
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "l")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Plan(filepath.Join(link, "f"), []byte("x"), 0o644); err != nil {
		t.Errorf("symlink outside home and repo refused: %v", err)
	}
}

func TestStateLoadedLazily(t *testing.T) {
	home := t.TempDir()
	statePath := StatePath(home)
	mustWrite(t, statePath, "{nope")
	in := &Installer{Paths: config.Paths{Home: home}, StatePath: statePath}

	if _, err := in.Plan(filepath.Join(home, "f"), nil, 0o644); err == nil || !strings.Contains(err.Error(), statePath) {
		t.Errorf("Plan err = %v, want one naming the state file", err)
	}
	if _, err := in.Tracked(); err == nil {
		t.Error("Tracked: expected error")
	}

	mustWrite(t, statePath, `{"files": {"/x": "abc"}}`)
	keys, err := in.Tracked()
	if err != nil || len(keys) != 1 || keys[0] != "/x" {
		t.Errorf("Tracked = %v, %v", keys, err)
	}
}

func TestLoadStateCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "files.json")
	mustWrite(t, p, "{nope")
	if _, err := LoadState(p); err == nil {
		t.Error("expected error for corrupt state")
	}
}

func TestPlanRemove(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, in *Installer, dst string)
		wantDetail string
		wantGone   bool
		wantBackup string
	}{
		{
			name:       "unchanged copy is deleted",
			setup:      func(t *testing.T, in *Installer, dst string) { converge(t, in, dst, "v1", 0o644) },
			wantDetail: "no longer managed",
			wantGone:   true,
		},
		{
			name: "edited copy is backed up",
			setup: func(t *testing.T, in *Installer, dst string) {
				converge(t, in, dst, "v1", 0o644)
				edit(t, dst, "my edit")
			},
			wantDetail: "no longer managed, edited: back up to .zshrc.zakwas-bak",
			wantGone:   true,
			wantBackup: "my edit",
		},
		{
			name: "file now owned elsewhere is left alone",
			setup: func(t *testing.T, in *Installer, dst string) {
				converge(t, in, dst, "v1", 0o644)
				if err := unlock(dst); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(dst); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/elsewhere", dst); err != nil {
					t.Fatal(err)
				}
			},
			wantDetail: "no longer managed, left in place",
		},
		{
			name: "already deleted file is only forgotten",
			setup: func(t *testing.T, in *Installer, dst string) {
				converge(t, in, dst, "v1", 0o644)
				if err := unlock(dst); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(dst); err != nil {
					t.Fatal(err)
				}
			},
			wantDetail: "already gone, forget",
			wantGone:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, home := newInstaller(t, true)
			dst := filepath.Join(home, ".zshrc")
			tt.setup(t, in, dst)

			c, err := in.PlanRemove(dst)
			if err != nil {
				t.Fatal(err)
			}
			if c.Action != engine.Remove || c.Detail != tt.wantDetail {
				t.Fatalf("change = %s, want detail %q", c, tt.wantDetail)
			}
			if err := c.Apply(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, ok := in.State.Get(dst); ok {
				t.Error("state still records dst")
			}
			if reloaded, _ := LoadState(StatePath(home)); len(reloaded.Files) != 0 {
				t.Errorf("forget not persisted: %v", reloaded.Files)
			}
			_, err = os.Lstat(dst)
			if gone := errors.Is(err, fs.ErrNotExist); gone != tt.wantGone {
				t.Errorf("gone = %v, want %v", gone, tt.wantGone)
			}
			if tt.wantBackup != "" {
				if got, err := os.ReadFile(dst + ".zakwas-bak"); err != nil || string(got) != tt.wantBackup {
					t.Errorf("backup = %q, %v", got, err)
				}
			}
		})
	}
}

func TestDiffs(t *testing.T) {
	in, home := newInstaller(t, false)
	dst := filepath.Join(home, "f")

	c := plan(t, in, dst, "a\nb\n", 0o644)
	if !strings.Contains(c.Diff, "+a") || !strings.Contains(c.Diff, "+b") {
		t.Errorf("create diff = %q", c.Diff)
	}
	if err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	c = plan(t, in, dst, "a\nc\n", 0o644)
	if !strings.Contains(c.Diff, "-b") || !strings.Contains(c.Diff, "+c") || strings.Contains(c.Diff, "+a") {
		t.Errorf("update diff = %q", c.Diff)
	}
}

func TestTracksMatchingUntrackedFile(t *testing.T) {
	in, home := newInstaller(t, false)
	dst := filepath.Join(home, "f")
	mustWrite(t, dst, "same")
	if err := os.Chmod(dst, 0o444); err != nil {
		t.Fatal(err)
	}
	c := converge(t, in, dst, "same", 0o644)
	if c == nil || c.Detail != "already matches, start tracking" {
		t.Fatalf("change = %v", c)
	}
	if sum, _ := in.State.Get(dst); sum != Sum([]byte("same")) {
		t.Error("not recorded")
	}
}

func TestPlanRemoveOutsideHome(t *testing.T) {
	in, _ := newInstaller(t, false)
	outside := filepath.Join(t.TempDir(), "f")
	mustWrite(t, outside, "keep")
	if err := in.State.Record(outside, Sum([]byte("keep"))); err != nil {
		t.Fatal(err)
	}
	c, err := in.PlanRemove(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("file outside $HOME must never be deleted: %v", err)
	}
}
