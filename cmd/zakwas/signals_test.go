package main

import (
	"context"
	"os"
	"os/signal"
	"slices"
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
	none := func(os.Signal) bool { return false }
	tests := []struct {
		name    string
		args    []string
		ignored func(os.Signal) bool
		want    []os.Signal
	}{
		{"plan", []string{"plan"}, none, []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}},
		{"apply", []string{"-y", "apply"}, none, []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}},
		{"no args", nil, none, []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}},
		{"mcp handles SIGTERM and SIGHUP itself", []string{"mcp"}, none, []os.Signal{syscall.SIGINT}},
		{"nohup", []string{"apply"}, func(s os.Signal) bool { return s == syscall.SIGHUP }, []os.Signal{syscall.SIGINT, syscall.SIGTERM}},
		{"ignored SIGTERM", []string{"apply"}, func(s os.Signal) bool { return s == syscall.SIGTERM }, []os.Signal{syscall.SIGINT, syscall.SIGHUP}},
		{"a background job keeps taking SIGINT, as before", []string{"apply"}, func(s os.Signal) bool { return s == syscall.SIGINT }, []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stopSignals(tt.args, tt.ignored); !slices.Equal(got, tt.want) {
				t.Errorf("signals = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStopSignalsSeesAnInheritedIgnore(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	defer signal.Reset(syscall.SIGHUP)
	if slices.Contains(stopSignals([]string{"apply"}, signal.Ignored), os.Signal(syscall.SIGHUP)) {
		t.Error("SIGHUP is taken although the process ignores it")
	}
}

func TestWatchSignalsCancelsOnEachStopSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop, caught := watchSignals(context.Background(), func(os.Signal) {}, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
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
			if got, want := context.Cause(ctx).Error(), sig.String()+" signal received"; got != want {
				t.Errorf("cause = %q, want %q", got, want)
			}
		})
	}
}

func TestWatchSignalsEndsTheRunThatIsStuckAfterTermOrHangup(t *testing.T) {
	for _, tt := range []struct {
		sig  syscall.Signal
		dies bool
	}{
		{syscall.SIGTERM, true},
		{syscall.SIGHUP, true},
		{syscall.SIGINT, false},
	} {
		t.Run(tt.sig.String(), func(t *testing.T) {
			died := make(chan os.Signal, 1)
			_, stop, _ := watchSignals(context.Background(), func(s os.Signal) { died <- s }, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			if err := syscall.Kill(os.Getpid(), tt.sig); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-died:
				if !tt.dies || got != tt.sig {
					t.Errorf("died of %v", got)
				}
			case <-time.After(forceExitAfter + 3*time.Second):
				if tt.dies {
					t.Errorf("a run stuck on a read outlived %v", tt.sig)
				}
			}
		})
	}
}

func TestWatchSignalsStopIsQuietAndRepeatable(t *testing.T) {
	died := make(chan os.Signal, 1)
	ctx, stop, caught := watchSignals(context.Background(), func(s os.Signal) { died <- s }, os.Interrupt)
	stop()
	if ctx.Err() == nil {
		t.Error("stop left the context running")
	}
	if caught() != nil {
		t.Errorf("caught %v after a plain stop", caught())
	}
}
