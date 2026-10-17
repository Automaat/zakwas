//go:build !windows

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestExecGroupStopsEverythingOnTimeout(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{"shell that forks its last command", "sleep 20; true"},
		{"children that ignore SIGINT", "trap '' INT; sleep 20 & echo $! > $PIDFILE; sleep 21; true"},
		{"orphan that left the shell", "(sleep 20 & echo $! > $PIDFILE); sleep 21; true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			e := &Exec{Detached: true, Grace: 300 * time.Millisecond}
			_, err := e.Run(ctx, Cmd{Name: "sh", Args: []string{"-c", tt.script}, Env: []string{"PIDFILE=" + pidfile}, Group: true})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("err = %v, want deadline exceeded", err)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("took %s; the command outlived the timeout", d)
			}
			data, err := os.ReadFile(pidfile)
			if err != nil {
				return
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for alive(pid) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if alive(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("background child %d survived the timeout", pid)
			}
		})
	}
}

func TestExecGroupOnlyWithoutATerminal(t *testing.T) {
	pgid := func(t *testing.T, e *Exec, group bool) int {
		t.Helper()
		out, err := Output(context.Background(), e, Cmd{Name: "sh", Args: []string{"-c", "ps -o pgid= -p $$"}, Group: group})
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			t.Fatalf("pgid %q: %v", out, err)
		}
		return n
	}
	ours := syscall.Getpgrp()
	tests := []struct {
		name     string
		exec     *Exec
		group    bool
		sameAsUs bool
	}{
		{"terminal, group asked", &Exec{}, true, true},
		{"no terminal, no group asked", &Exec{Detached: true}, false, true},
		{"no terminal, group asked", &Exec{Detached: true}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pgid(t, tt.exec, tt.group); (got == ours) != tt.sameAsUs {
				t.Errorf("child pgid %d, ours %d, want same group = %v", got, ours, tt.sameAsUs)
			}
		})
	}
}
