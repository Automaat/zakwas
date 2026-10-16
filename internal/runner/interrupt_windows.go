//go:build windows

package runner

import (
	"os"
	"os/exec"
	"time"
)

func interrupt(p *os.Process) error { return p.Kill() }

func hasTerminal() bool { return false }

func ownGroup(*exec.Cmd, time.Duration) func() { return func() {} }
