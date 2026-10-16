//go:build windows

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shimDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInstalledFindsCmdShims(t *testing.T) {
	shimDir(t, map[string]string{"zakwas-fake-agent.cmd": "@echo off\r\nexit /b 0\r\n"})
	e := NewExec()
	if !e.Installed("zakwas-fake-agent") {
		t.Error("a .cmd shim on PATH is not found without its extension")
	}
	if e.Installed("zakwas-no-such-agent") {
		t.Error("found a binary that does not exist")
	}
}

func TestRunCmdShimWithArguments(t *testing.T) {
	shimDir(t, map[string]string{"zakwas-fake-agent.cmd": "@echo off\r\necho got %1 %2\r\nexit /b 3\r\n"})
	res, err := NewExec().Run(context.Background(), Cmd{Name: "zakwas-fake-agent", Args: []string{"plugin", "list"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "got plugin list" || res.ExitCode != 3 {
		t.Errorf("stdout %q, exit %d", res.Stdout, res.ExitCode)
	}
}

func TestExecCancelKillsChildOnWindows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := NewExec().Run(ctx, Cmd{Name: "ping", Args: []string{"-n", "60", "127.0.0.1"}})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if d := time.Since(start); d >= WaitDelay {
		t.Errorf("took %s; the child should be killed when the context ends", d)
	}
}
