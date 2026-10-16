//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

var terminationSignals = []os.Signal{syscall.SIGTERM}

// ignoreBrokenPipe turns a write to a closed stdout into an error instead
// of killing the process, and returns the undo.
func ignoreBrokenPipe() func() {
	signal.Ignore(syscall.SIGPIPE)
	return func() { signal.Reset(syscall.SIGPIPE) }
}
