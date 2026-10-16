//go:build windows

package cli

import "os"

var terminationSignals = []os.Signal{os.Interrupt}

// ignoreBrokenPipe does nothing: Windows has no SIGPIPE, a write to a closed
// pipe already fails with an error.
func ignoreBrokenPipe() func() { return func() {} }
