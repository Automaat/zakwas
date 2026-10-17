//go:build windows

package runner

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// interrupt kills the command and what it started. A Windows child can't be
// sent SIGINT, and an npm .cmd shim runs cmd.exe, which runs node: killing
// only the shim leaves node holding the output pipes, so Run would wait out
// the whole WaitDelay.
func interrupt(p *os.Process) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run(); err == nil {
		return nil
	}
	return p.Kill()
}

func setCmdLine(cmd *exec.Cmd, line string) {
	if line != "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	}
}

var consoleMu sync.Mutex

// setConsoleCP serializes the commands that change the console code page, so
// one's restore can't land in the middle of another's run.
func setConsoleCP(cp uint32) func() {
	consoleMu.Lock()
	restore := switchCodePage(func() uint32 {
		cur, _ := windows.GetConsoleOutputCP()
		return cur
	}, func(v uint32) { _ = windows.SetConsoleOutputCP(v) }, cp)
	return func() {
		restore()
		consoleMu.Unlock()
	}
}

func hasTerminal() bool { return false }

func ownGroup(*exec.Cmd, time.Duration) func() { return func() {} }
