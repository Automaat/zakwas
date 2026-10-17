package commands

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
)

// maxCommandLine is what CreateProcess accepts on Windows, in UTF-16 units.
const maxCommandLine = 32767

// labelWidth is how much of a script an error message shows.
const labelWidth = 80

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
		run.Label = sh + " -Command " + shorten(script)
		run.KeepConsoleCP = true
	case config.ShellCmd:
		line := strings.TrimRight(script, " \t\r\n")
		run.Name, run.Args = "cmd.exe", []string{"/d", "/s", "/c", line}
		run.CmdLine = `cmd.exe /d /s /c "` + line + `"`
		run.Label = "cmd /c " + shorten(line)
	default:
		return run, fmt.Errorf("%s: unknown shell %q", c.Name, sh)
	}
	if (run.Name != "sh" || c.Shell != "" || m.goos() == platform.Windows) && !m.Runner.Installed(run.Name) {
		return run, fmt.Errorf("%s: shell %s is not installed (%s not found on PATH)", c.Name, sh, run.Name)
	}
	if m.goos() == platform.Windows {
		if n := commandLineLength(run); n > maxCommandLine {
			return run, fmt.Errorf("%s: the %s command line is %d characters, over the %d Windows allows; put the script in a file and call it (& ./script.ps1)", c.Name, sh, n, maxCommandLine)
		}
	}
	return run, nil
}

// shorten is the first line of script, cut to labelWidth, for messages.
func shorten(script string) string {
	first, rest, multi := strings.Cut(strings.TrimSpace(script), "\n")
	first = strings.TrimRight(first, "\r")
	if len([]rune(first)) > labelWidth {
		first = string([]rune(first)[:labelWidth])
		multi = true
	}
	if multi || rest != "" {
		return first + "…"
	}
	return first
}

// commandLineLength is an upper bound of the command line Go builds for run
// on Windows: every argument quoted, quotes and backslashes escaped.
func commandLineLength(run runner.Cmd) int {
	if run.CmdLine != "" {
		return len(utf16.Encode([]rune(run.CmdLine)))
	}
	n := len(utf16.Encode([]rune(run.Name)))
	for _, a := range run.Args {
		n += len(utf16.Encode([]rune(a))) + 3 + strings.Count(a, `"`) + strings.Count(a, `\`)
	}
	return n
}

// The PowerShell wrapper. The script runs dot-sourced in a script block, so
// its variables and functions behave as at the top level, and no try block
// turns its statement errors into aborts. $LASTEXITCODE gets a validation
// attribute that notes the newest error each time anything, the engine
// included, assigns it: when the newest error is still that one at the
// end, no cmdlet failed since the last native command, so a failure is that
// command's. A cmdlet that failed later is exit code 1. The console code
// page the wrapper changes is put back by the runner (KeepConsoleCP), which
// also covers `exit`, `throw` and a kill.
const (
	psPrologue = "$ProgressPreference='SilentlyContinue';try{$PSStyle.OutputRendering='PlainText'}catch{};" +
		"try{[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);$OutputEncoding=[Console]::OutputEncoding}catch{};" +
		"$zkTop=$null;$zkOK=$true;$zkCode=$null;$zkFresh=$false;" +
		"[ValidateScript({$global:zkTop=if($Error.Count){$Error[0]}else{$null};$true})]$global:LASTEXITCODE=0;" +
		". {\n"
	psEpilogue = "\n$zkOK=$?;$zkCode=$LASTEXITCODE;$zkFresh=[object]::ReferenceEquals($(if($Error.Count){$Error[0]}else{$null}),$zkTop)};" +
		"if($zkOK){exit 0}elseif($zkCode -and $zkFresh){exit $zkCode}else{exit 1}"
)

// psScript wraps script. A script's leading using statements (and the
// comments and blank lines before them) move above the wrapper, where
// PowerShell requires them.
func psScript(script string) string {
	head, body := splitUsing(script)
	return head + psPrologue + body + psEpilogue
}

// splitUsing cuts script after its last leading using statement.
func splitUsing(script string) (head, body string) {
	lines := strings.SplitAfter(script, "\n")
	end := 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case isUsing(t):
			end = i + 1
		case t == "" || strings.HasPrefix(t, "#"):
		default:
			return strings.Join(lines[:end], ""), strings.Join(lines[end:], "")
		}
	}
	return strings.Join(lines[:end], ""), strings.Join(lines[end:], "")
}

func isUsing(line string) bool {
	word, rest, ok := strings.Cut(strings.ToLower(line), " ")
	if !ok || word != "using" {
		return false
	}
	kind, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	switch kind {
	case "namespace", "module", "assembly", "type":
		return true
	}
	return false
}
