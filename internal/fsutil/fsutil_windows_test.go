//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenameWaitsForAProcessHoldingTheDestination(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	write(t, from, "new")
	write(t, to, "old")
	held, err := os.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = held.Close()
	}()

	if err := Rename(from, to); err != nil {
		t.Fatalf("a destination held open for a moment must not fail the rename: %v", err)
	}

	if got, _ := os.ReadFile(to); string(got) != "new" {
		t.Errorf("to = %q, want new", got)
	}
}

func TestRenameGivesUpOnADestinationHeldOpen(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	write(t, from, "new")
	write(t, to, "old")
	held, err := os.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	if err := Rename(from, to); err == nil {
		t.Fatal("rename over a file held open for good succeeded")
	}

	if got, _ := os.ReadFile(from); string(got) != "new" {
		t.Errorf("the source was lost: %q", got)
	}
}
