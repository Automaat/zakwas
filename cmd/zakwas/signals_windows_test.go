//go:build windows

package main

import (
	"os"
	"testing"
)

func TestWindowsStopsOnInterruptOnly(t *testing.T) {
	for _, args := range [][]string{{"plan"}, {"apply", "-y"}, {"mcp"}, nil} {
		got := stopSignals(args, func(os.Signal) bool { return false })
		if len(got) != 1 || got[0] != os.Interrupt {
			t.Errorf("stopSignals(%v) = %v, want only os.Interrupt", args, got)
		}
	}
}

func TestWindowsInterruptExitsWith130(t *testing.T) {
	if got := exitCodeFor(os.Interrupt); got != exitInterrupted {
		t.Errorf("exitCodeFor(os.Interrupt) = %d, want %d", got, exitInterrupted)
	}
}
