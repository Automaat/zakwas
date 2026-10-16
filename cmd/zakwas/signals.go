package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// exitInterrupted follows the shell convention of 128 + SIGINT.
const exitInterrupted = 130

// stopSignals are the signals that end a run: Ctrl-C, a supervisor or
// timeout(1) asking it to stop, and a closed terminal. mcp keeps SIGTERM and
// SIGHUP out, because it stops on SIGTERM by itself and exits 0.
func stopSignals(args []string) []os.Signal {
	if len(args) > 0 && args[0] == "mcp" {
		return []os.Signal{os.Interrupt}
	}
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

// watchSignals cancels the context on the first of sigs and restores the
// default action, so a second signal kills zakwas instead of being
// swallowed. A cancelled context interrupts the commands in flight, which
// matters for those in a process group of their own: they never see a
// signal sent to zakwas's group. caught names the signal that ended the run.
func watchSignals(parent context.Context, sigs ...os.Signal) (ctx context.Context, stop func(), caught func() os.Signal) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	var got atomic.Value
	done := make(chan struct{})
	go func() {
		select {
		case s := <-ch:
			got.Store(s)
			signal.Stop(ch)
			cancel()
		case <-done:
		}
	}()
	stop = func() {
		signal.Stop(ch)
		close(done)
		cancel()
	}
	caught = func() os.Signal {
		s, _ := got.Load().(os.Signal)
		return s
	}
	return ctx, stop, caught
}

// exitCodeFor is 128 plus the signal number: 130 for SIGINT, 143 for SIGTERM
// and 129 for SIGHUP.
func exitCodeFor(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return exitInterrupted
}
