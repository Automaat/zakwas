//go:build !windows

package runner

import "os"

func interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }
