//go:build windows

package selfupdate

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runningCopy starts a copy of ping.exe named zakwas.exe, which keeps
// running for a minute, and stops it when the test ends.
func runningCopy(t *testing.T) string {
	t.Helper()
	src, err := os.Open(filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe"))
	if err != nil {
		t.Skipf("no ping.exe to stand in for a running zakwas: %v", err)
	}
	defer func() { _ = src.Close() }()
	exe := filepath.Join(t.TempDir(), "zakwas.exe")
	dst, err := os.Create(exe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-n", "60", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	time.Sleep(300 * time.Millisecond)
	return exe
}

func TestReplaceSwapsABinaryThatIsRunning(t *testing.T) {
	exe := runningCopy(t)

	if err := Replace(exe, []byte("new")); err != nil {
		t.Fatalf("a running zakwas.exe must be replaceable: %v", err)
	}

	if got := read(t, exe); got != "new" {
		t.Errorf("exe = %q, want new", got)
	}
	if err := os.Remove(exe + OldSuffix); err == nil {
		t.Errorf("Windows removed the running binary; ReplaceRunning exists because it cannot")
	}
}

func TestReplaceRefusesWhileTheLastOldBinaryRuns(t *testing.T) {
	exe := runningCopy(t)
	if err := Replace(exe, []byte("v2")); err != nil {
		t.Fatal(err)
	}

	err := Replace(exe, []byte("v3"))

	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Errorf("err = %v, want the old binary reported as in use", err)
	}
	if got := read(t, exe); got != "v2" {
		t.Errorf("exe = %q, want v2 untouched", got)
	}
}

func TestManagedByIgnoresTheCaseOfAWindowsPath(t *testing.T) {
	local := filepath.Join(t.TempDir(), "AppData", "Local")
	exe := strings.ToUpper(filepath.Join(local, "mise", "installs", "zakwas", "1.0.0", "zakwas.exe"))
	getenv := func(k string) string { return map[string]string{"LOCALAPPDATA": local}[k] }

	if got := ManagedBy(exe, t.TempDir(), getenv); !strings.Contains(got, "mise") {
		t.Errorf("ManagedBy(%q) = %q", exe, got)
	}
}
