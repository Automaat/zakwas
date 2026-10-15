package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
)

func TestGitCloneReplacesWithoutGap(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
		rounds   int
	}{
		{name: "first fetch", rounds: 1},
		{name: "replacing a fetched copy", existing: true, rounds: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, r := newOpencode(t, config.Agents{})
			cacheDir := filepath.Join(t.TempDir(), "cache")
			dir := filepath.Join(cacheDir, "sai")
			if tt.existing {
				writeFile(t, filepath.Join(dir, "old.txt"), "old")
				if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			r.repos["https://github.com/o/sai.git"] = func(clone string) {
				writeFile(t, filepath.Join(clone, "new.txt"), "new")
			}

			var gaps atomic.Int64
			stop := make(chan struct{})
			var watcher sync.WaitGroup
			if tt.existing {
				watcher.Go(func() {
					for {
						select {
						case <-stop:
							return
						default:
						}
						time.Sleep(20 * time.Microsecond)
						if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
							gaps.Add(1)
						}
					}
				})
			}
			for round := range tt.rounds {
				if err := gitClone(context.Background(), m.Runner, cacheDir, "sai", "o/sai"); err != nil {
					t.Fatal(err)
				}
				if tt.existing && round < tt.rounds-1 {
					writeFile(t, filepath.Join(dir, "old.txt"), "old")
					if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			close(stop)
			watcher.Wait()

			if n := gaps.Load(); n != 0 {
				t.Errorf("the marketplace dir was missing in %d of the checks that ran during the swaps", n)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "new.txt")); err != nil || string(got) != "new" {
				t.Errorf("new.txt = %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
				t.Errorf("old copy not replaced: %v", err)
			}
			if entries, _ := os.ReadDir(cacheDir); len(entries) != 1 {
				t.Errorf("cache holds leftovers: %v", entries)
			}
		})
	}
}

func TestRenameAsideReplacesDir(t *testing.T) {
	tests := []struct {
		name    string
		dir     bool
		wantErr bool
	}{
		{name: "replaces the old copy", dir: true},
		{name: "fails and keeps nothing when dir is missing", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir, next := filepath.Join(root, "sai"), filepath.Join(root, ".sai.zakwas-1")
			writeFile(t, filepath.Join(next, "new.txt"), "new")
			if tt.dir {
				writeFile(t, filepath.Join(dir, "old.txt"), "old")
			}
			err := renameAside(next, dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
				t.Errorf("new copy not in place: %v", err)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 1 {
				t.Errorf("leftovers: %v", entries)
			}
		})
	}
}

func TestReplaceDirFallsBackAndRecovers(t *testing.T) {
	tests := []struct {
		name     string
		existing bool
		exchange func(dir string) func(a, b string) error
		wantErr  error
		wantNew  bool
	}{
		{
			name:     "exchange not supported falls back to renaming aside",
			existing: true,
			exchange: func(string) func(a, b string) error {
				return func(a, b string) error { return syscall.ENOTSUP }
			},
			wantNew: true,
		},
		{
			name:     "exchange rejected as invalid falls back to renaming aside",
			existing: true,
			exchange: func(string) func(a, b string) error {
				return func(a, b string) error { return syscall.EINVAL }
			},
			wantNew: true,
		},
		{
			name:     "exchange unsupported on the platform falls back",
			existing: true,
			exchange: func(string) func(a, b string) error {
				return func(a, b string) error { return errors.ErrUnsupported }
			},
			wantNew: true,
		},
		{
			name:     "other exchange errors are returned and keep the old copy",
			existing: true,
			exchange: func(string) func(a, b string) error {
				return func(a, b string) error { return syscall.EPERM }
			},
			wantErr: syscall.EPERM,
		},
		{
			name:     "dir pruned after the check is renamed into place",
			existing: true,
			exchange: func(dir string) func(a, b string) error {
				return func(a, b string) error {
					if err := os.RemoveAll(dir); err != nil {
						return err
					}
					return syscall.ENOENT
				}
			},
			wantNew: true,
		},
		{
			name: "missing dir is renamed into place without an exchange",
			exchange: func(string) func(a, b string) error {
				return func(a, b string) error { return errors.New("exchange must not run") }
			},
			wantNew: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir, next := filepath.Join(root, "sai"), filepath.Join(root, ".sai.zakwas-1")
			writeFile(t, filepath.Join(next, "new.txt"), "new")
			if tt.existing {
				writeFile(t, filepath.Join(dir, "old.txt"), "old")
			}
			err := replaceDirWith(tt.exchange(dir), next, dir)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			_, newErr := os.Stat(filepath.Join(dir, "new.txt"))
			_, oldErr := os.Stat(filepath.Join(dir, "old.txt"))
			if tt.wantNew && (newErr != nil || oldErr == nil) {
				t.Errorf("new copy not in place: new %v, old %v", newErr, oldErr)
			}
			if !tt.wantNew && (newErr == nil || oldErr != nil) {
				t.Errorf("old copy not kept: new %v, old %v", newErr, oldErr)
			}
			if entries, _ := os.ReadDir(root); tt.wantNew && len(entries) != 1 {
				t.Errorf("leftovers: %v", entries)
			}
		})
	}
}
