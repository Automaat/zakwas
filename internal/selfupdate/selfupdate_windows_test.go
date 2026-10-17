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

var running *exec.Cmd

func stopRunning(t *testing.T) {
	t.Helper()
	_ = running.Process.Kill()
	_ = running.Wait()
}

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
	running = cmd
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	time.Sleep(300 * time.Millisecond)
	return exe
}

func TestReplaceSwapsABinaryThatIsRunning(t *testing.T) {
	exe := runningCopy(t)
	home := t.TempDir()

	if err := Replace(exe, home, []byte("new")); err != nil {
		t.Fatalf("a running zakwas.exe must be replaceable: %v", err)
	}

	if got := read(t, exe); got != "new" {
		t.Errorf("exe = %q, want new", got)
	}
	old := recorded(t, StatePath(home))
	if len(old) != 1 {
		t.Fatalf("recorded %q", old)
	}
	if err := os.Remove(old[0]); err == nil {
		t.Errorf("Windows removed the running binary; keeping the old copy exists because it cannot")
	}
}

func TestReplaceTwiceWhileTheFirstOldCopyRuns(t *testing.T) {
	exe := runningCopy(t)
	home := t.TempDir()
	if err := Replace(exe, home, []byte("v2")); err != nil {
		t.Fatal(err)
	}

	if err := Replace(exe, home, []byte("v3")); err != nil {
		t.Fatalf("a second update must not wait for the copy the first one left: %v", err)
	}

	if got := read(t, exe); got != "v3" {
		t.Errorf("exe = %q, want v3", got)
	}
	if got := len(recorded(t, StatePath(home))); got != 2 {
		t.Errorf("recorded %d old copies, want 2", got)
	}
}

func TestCleanupOldRemovesTheCopyOnceItStopsRunning(t *testing.T) {
	exe := runningCopy(t)
	home := t.TempDir()
	if err := Replace(exe, home, []byte("new")); err != nil {
		t.Fatal(err)
	}
	old := recorded(t, StatePath(home))[0]

	cleanupOld(StatePath(home), exe)
	if _, err := os.Lstat(old); err != nil {
		t.Fatalf("cleanup removed a copy that is still running: %v", err)
	}
	if len(recorded(t, StatePath(home))) != 1 {
		t.Fatalf("the running copy was forgotten")
	}

	stopRunning(t)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cleanupOld(StatePath(home), exe)
		if _, err := os.Lstat(old); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("%s is still there after the process stopped", old)
}

func TestManagedByIgnoresTheCaseOfAWindowsPath(t *testing.T) {
	local := filepath.Join(t.TempDir(), "AppData", "Local")
	exe := strings.ToUpper(filepath.Join(local, "mise", "installs", "zakwas", "1.0.0", "zakwas.exe"))
	getenv := func(k string) string { return map[string]string{"LOCALAPPDATA": local}[k] }

	if got := ManagedBy(exe, t.TempDir(), getenv); !strings.Contains(got, "mise") {
		t.Errorf("ManagedBy(%q) = %q", exe, got)
	}
}
