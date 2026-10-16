//go:build windows

package lock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestAliveOnWindows(t *testing.T) {
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		pid  int
		want bool
	}{
		{"this process", os.Getpid(), true},
		{"an exited process", gone.Process.Pid, false},
		{"zero", 0, false},
		{"negative", -4, false},
		{"beyond a DWORD", 1 << 40, false},
	}
	for _, tt := range tests {
		if got := alive(tt.pid); got != tt.want {
			t.Errorf("%s: alive(%d) = %v, want %v", tt.name, tt.pid, got, tt.want)
		}
	}
}

func TestLockedByteLeavesTheHolderRecordReadable(t *testing.T) {
	home := t.TempDir()
	held := acquire(t, home, Options{})
	defer func() { _ = held.Release() }()

	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatalf("a held lock must not block reading who holds it: %v", err)
	}
	if got := readHolder(Path(home)); got.PID != os.Getpid() || got.Command != "zakwas apply" {
		t.Errorf("holder %+v from %q", got, data)
	}
}

func TestSecondHandleCannotLockWhileHeld(t *testing.T) {
	home := t.TempDir()
	held := acquire(t, home, Options{})
	defer func() { _ = held.Release() }()

	_, err := Acquire(context.Background(), home, "zakwas apply", Options{NoWait: true})

	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("got %v, want *BusyError", err)
	}
}
