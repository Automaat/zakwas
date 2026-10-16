package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRenameReplacesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	write(t, from, "new")
	write(t, to, "old")

	if err := Rename(from, to); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(to); string(got) != "new" {
		t.Errorf("to = %q, want new", got)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Errorf("the source is still there: %v", err)
	}
}

func TestRenameReplacesAReadOnlyFile(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	write(t, from, "new")
	write(t, to, "old")
	if err := os.Chmod(to, 0o444); err != nil {
		t.Fatal(err)
	}

	if err := Rename(from, to); err != nil {
		t.Fatalf("rename(2) replaces a read-only file, so Rename must too: %v", err)
	}

	if got, _ := os.ReadFile(to); string(got) != "new" {
		t.Errorf("to = %q, want new", got)
	}
}

func TestRenameFailsForAMissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "to")); !os.IsNotExist(err) {
		t.Errorf("err = %v, want not exist", err)
	}
}

func TestSyncDir(t *testing.T) {
	if err := SyncDir(t.TempDir()); err != nil {
		t.Errorf("SyncDir: %v", err)
	}
}
