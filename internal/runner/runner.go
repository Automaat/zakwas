// Package runner executes external commands behind an interface so modules
// can be tested without touching the real system.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Cmd describes one external command invocation. Env entries are appended to
// the current process environment. Stream sends output to the runner's
// writers instead of capturing it.
type Cmd struct {
	Name   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  string
	Stream bool
	// Group runs the command in its own process group and interrupts the
	// whole group on cancel, so a shell that forks its last command (dash)
	// doesn't outlive the timeout. Only for commands that never read a
	// terminal: a background group is stopped when it does.
	Group bool
}

// String renders the command the way a user would type it.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Result holds the outcome of a command that started.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Err reports a non-zero exit as an error, including stderr for context.
func (r Result) Err(c Cmd) error {
	if r.ExitCode == 0 {
		return nil
	}
	msg := strings.TrimSpace(r.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(r.Stdout)
	}
	return fmt.Errorf("%s: exit %d: %s", c, r.ExitCode, msg)
}

// Runner executes commands. Run returns an error only when the command could
// not be started; a non-zero exit is reported through Result.ExitCode.
// Installed reports whether a binary is on PATH, so modules can install
// their own prerequisites (brew, mise) on a fresh machine.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
	Installed(name string) bool
}

// Exec runs commands on the host. Env entries go to every command.
type Exec struct {
	Stdout  io.Writer
	Stderr  io.Writer
	Env     []string
	NoStdin bool
}

// NewExec returns an Exec streaming to the process stdout/stderr.
func NewExec() *Exec {
	return &Exec{Stdout: os.Stdout, Stderr: os.Stderr}
}

// WaitDelay is how long a cancelled command gets to exit after SIGINT
// before it is killed.
const WaitDelay = 30 * time.Second

func (e *Exec) Installed(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Run interrupts the command when ctx is cancelled rather than killing it:
// SIGKILL would leave brew or mise mid-install with locks and partial kegs.
func (e *Exec) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	if c.Group && !c.Stream {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		}
	}
	cmd.WaitDelay = WaitDelay
	cmd.Dir = c.Dir
	cmd.Env = slices.Concat(os.Environ(), e.Env, c.Env)
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	} else if c.Stream && !e.NoStdin {
		cmd.Stdin = os.Stdin
	}

	var stdout, stderr bytes.Buffer
	if c.Stream {
		cmd.Stdout, cmd.Stderr = e.Stdout, e.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("%s: %w", c, ctxErr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", c, err)
	}
	return res, nil
}

// Output runs c and returns stdout, failing on a non-zero exit.
func Output(ctx context.Context, r Runner, c Cmd) (string, error) {
	res, err := r.Run(ctx, c)
	if err != nil {
		return "", err
	}
	if err := res.Err(c); err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// Check runs c and fails on a non-zero exit.
func Check(ctx context.Context, r Runner, c Cmd) error {
	_, err := Output(ctx, r, c)
	return err
}
