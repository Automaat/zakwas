package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// exitInterrupted follows the shell convention of 128 + SIGINT.
const exitInterrupted = 130

// forceExitAfter is how long a run gets to wind down after SIGTERM or
// SIGHUP before zakwas dies of the signal: it may be blocked on a read
// (a confirmation prompt) that no context can interrupt.
const forceExitAfter = 2 * time.Second

// stopSignals are the signals that end a run: Ctrl-C, a supervisor or
// timeout(1) asking it to stop, and a closed terminal. SIGINT and SIGTERM
// are always taken, as before. SIGHUP is left alone when the process started
// with it ignored (nohup); the Go runtime only keeps SIGHUP and SIGINT
// ignored across start-up, so ignored never reports SIGTERM. mcp stops on
// SIGTERM and SIGHUP by itself and exits 0.
func stopSignals(args []string, ignored func(os.Signal) bool) []os.Signal {
	sigs := []os.Signal{os.Interrupt}
	if len(args) > 0 && args[0] == "mcp" {
		return sigs
	}
	sigs = append(sigs, syscall.SIGTERM)
	if !ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	return sigs
}

// watchSignals cancels the context on the first of sigs and restores the
// default action, so a second signal kills zakwas instead of being
// swallowed. A cancelled context interrupts the commands in flight, which
// matters for those in a process group of their own: they never see a
// signal sent to zakwas's group. caught names the signal that ended the
// run. After SIGTERM or SIGHUP, a run that has not returned within
// forceExitAfter dies of the signal through die, as it did before
// zakwas handled them; SIGINT keeps waiting for the run.
func watchSignals(parent context.Context, die func(os.Signal), sigs ...os.Signal) (ctx context.Context, stop func(), caught func() os.Signal) {
	ctx, cancel := context.WithCancelCause(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)
	var got atomic.Value
	done := make(chan struct{})
	var force atomic.Pointer[time.Timer]
	go func() {
		select {
		case s := <-ch:
			got.Store(s)
			signal.Stop(ch)
			cancel(fmt.Errorf("%v signal received", s))
			if s != os.Interrupt {
				force.Store(time.AfterFunc(forceExitAfter, func() { die(s) }))
			}
		case <-done:
		}
	}()
	stop = func() {
		signal.Stop(ch)
		close(done)
		cancel(nil)
		if t := force.Load(); t != nil {
			t.Stop()
		}
	}
	caught = func() os.Signal {
		s, _ := got.Load().(os.Signal)
		return s
	}
	return ctx, stop, caught
}

// dieOf ends the process the way an unhandled signal would.
func dieOf(sig os.Signal) {
	signal.Reset(sig)
	if s, ok := sig.(syscall.Signal); ok {
		_ = syscall.Kill(os.Getpid(), s)
	}
	time.Sleep(time.Second)
	os.Exit(exitCodeFor(sig))
}

// exitCodeFor is 128 plus the signal number: 130 for SIGINT, 143 for SIGTERM
// and 129 for SIGHUP.
func exitCodeFor(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return exitInterrupted
}
