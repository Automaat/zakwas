//go:build !windows

package runner

import (
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

func interrupt(p *os.Process) error { return p.Signal(os.Interrupt) }

func hasTerminal() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

// ownGroup runs cmd in its own process group, interrupted on cancel and
// killed after grace. The returned func kills what is left once Run is done.
func ownGroup(cmd *exec.Cmd, grace time.Duration) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var killer atomic.Pointer[time.Timer]
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		killer.Store(time.AfterFunc(grace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }))
		if err := syscall.Kill(-pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	return func() {
		if t := killer.Load(); t != nil {
			t.Stop()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}
