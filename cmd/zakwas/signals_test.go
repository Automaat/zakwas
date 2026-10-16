package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		sig  os.Signal
		want int
	}{
		{syscall.SIGINT, 130},
		{syscall.SIGTERM, 143},
		{syscall.SIGHUP, 129},
	}
	for _, tt := range tests {
		if got := exitCodeFor(tt.sig); got != tt.want {
			t.Errorf("exitCodeFor(%v) = %d, want %d", tt.sig, got, tt.want)
		}
	}
}

func TestStopSignals(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"plan", []string{"plan"}, 3},
		{"apply", []string{"-y", "apply"}, 3},
		{"no args", nil, 3},
		{"mcp handles SIGTERM itself", []string{"mcp"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(stopSignals(tt.args)); got != tt.want {
				t.Errorf("%d signals, want %d", got, tt.want)
			}
		})
	}
}

func TestWatchSignalsCancelsOnEachStopSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop, caught := watchSignals(context.Background(), stopSignals([]string{"plan"})...)
			defer stop()
			if caught() != nil || ctx.Err() != nil {
				t.Fatal("cancelled before any signal")
			}
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("%v did not cancel the context", sig)
			}
			if got := caught(); got != sig {
				t.Errorf("caught %v, want %v", got, sig)
			}
			if code := exitCodeFor(caught()); code != 128+int(sig) {
				t.Errorf("exit %d", code)
			}
		})
	}
}

func TestWatchSignalsStopIsQuietAndRepeatable(t *testing.T) {
	ctx, stop, caught := watchSignals(context.Background(), os.Interrupt)
	stop()
	if ctx.Err() == nil {
		t.Error("stop left the context running")
	}
	if caught() != nil {
		t.Errorf("caught %v after a plain stop", caught())
	}
}
