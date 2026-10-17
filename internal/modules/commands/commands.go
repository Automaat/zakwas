// Package commands runs guarded shell snippets: `run` executes only while
// `check` fails. It is the escape hatch for setup no other module covers.
// Both run from Home, so relative paths don't depend on where zakwas started.
// A snippet runs with sh, or with PowerShell on Windows, unless the entry
// names a shell.
package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
)

// CheckTimeout bounds each `check`, so a hung one can't hang `zakwas plan`.
var CheckTimeout = 30 * time.Second

type Module struct {
	Commands []config.Command
	Home     string
	Runner   runner.Runner
	// GOOS is the operating system the commands run on; empty means this one.
	GOOS string
}

// shellFor is the shell c runs with: its own, else sh, else on Windows
// pwsh when installed and Windows PowerShell if not.
func (m *Module) shellFor(c config.Command) string {
	switch {
	case c.Shell != "":
		return c.Shell
	case m.goos() != platform.Windows:
		return config.ShellSh
	case m.Runner.Installed("pwsh"):
		return config.ShellPwsh
	}
	return config.ShellPowerShell
}

func (m *Module) goos() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return platform.Current()
}

// cmd builds the invocation of script in c's shell. sh stays exactly
// `sh -c script`. PowerShell never reads a profile or prompts, and reports
// a failing last statement or native command by its exit code. cmd.exe takes
// one line, handed over verbatim with /s so its quote stripping applies.
func (m *Module) cmd(c config.Command, script string) (runner.Cmd, error) {
	sh := m.shellFor(c)
	run := runner.Cmd{Dir: m.Home}
	switch sh {
	case config.ShellSh, config.ShellBash:
		run.Name, run.Args = sh, []string{"-c", script}
	case config.ShellPowerShell, config.ShellPwsh:
		run.Name = sh
		run.Args = []string{"-NoProfile", "-NonInteractive"}
		if m.goos() == platform.Windows {
			run.Args = append(run.Args, "-ExecutionPolicy", "Bypass")
		}
		run.Args = append(run.Args, "-Command", psScript(script))
		run.Label = sh + " -Command " + script
	case config.ShellCmd:
		line := strings.TrimRight(script, "\r\n")
		run.Name, run.Args = "cmd.exe", []string{"/d", "/s", "/c", line}
		run.CmdLine = `cmd.exe /d /s /c "` + line + `"`
		run.Label = "cmd /c " + line
	default:
		return run, fmt.Errorf("%s: unknown shell %q", c.Name, sh)
	}
	if run.Name != "sh" && !m.Runner.Installed(run.Name) {
		return run, fmt.Errorf("%s: shell %s is not installed (%s not found on PATH)", c.Name, sh, run.Name)
	}
	return run, nil
}

const (
	psPrologue = "$ProgressPreference='SilentlyContinue';$zakwasEnc=[Console]::OutputEncoding;try{[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);$OutputEncoding=[Console]::OutputEncoding}catch{}\n"
	psEpilogue = "\n$zakwasOK=$?;$zakwasCode=$LASTEXITCODE;try{[Console]::OutputEncoding=$zakwasEnc}catch{};if($zakwasOK){exit 0}elseif($zakwasCode){exit $zakwasCode}else{exit 1}"
)

// psScript wraps script so that PowerShell writes UTF-8 and exits like sh
// would: 0 when the last statement succeeded, else the native exit code.
// -Command alone reports only 0 or 1 for a native command.
func psScript(script string) string { return psPrologue + script + psEpilogue }

func (m *Module) Name() string { return "commands" }

func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	for _, c := range m.Commands {
		ok, err := m.satisfied(ctx, c)
		if err != nil {
			return nil, err
		}
		if ok {
			continue
		}
		changes = append(changes, engine.Change{Action: engine.Run, Target: c.Name, Diff: c.Run, Streams: true, Apply: m.apply(c)})
	}
	return changes, nil
}

func (m *Module) satisfied(ctx context.Context, c config.Command) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	check, err := m.cmd(c, c.Check)
	if err != nil {
		return false, err
	}
	check.Group = true
	res, err := m.Runner.Run(ctx, check)
	if errors.Is(err, context.DeadlineExceeded) {
		return false, fmt.Errorf("%s: check timed out after %s", c.Name, CheckTimeout)
	}
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// apply re-checks first: an earlier module (brew, mise) may have satisfied
// the check since planning.
func (m *Module) apply(c config.Command) func(context.Context) error {
	return func(ctx context.Context) error {
		ok, err := m.satisfied(ctx, c)
		if err != nil || ok {
			return err
		}
		run, err := m.cmd(c, c.Run)
		if err != nil {
			return err
		}
		run.Stream = true
		if err := runner.Check(ctx, m.Runner, run); err != nil {
			return err
		}
		if ok, err := m.satisfied(ctx, c); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("check %q still fails after run", c.Check)
		}
		return nil
	}
}
