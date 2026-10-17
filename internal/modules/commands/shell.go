package commands

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
)

// Windows hands a process at most 32,766 characters of command line and
// 32,767 per environment variable, both counting the terminating NUL; cmd.exe
// itself stops at 8,191.
const (
	maxWindowsArgs   = 32766
	maxWindowsEnvVar = 32766
	maxCmdLine       = 8191
	// maxUnixEnvVar is Linux's limit for one environment string.
	maxUnixEnvVar = 131071
)

// psScriptVar carries a PowerShell snippet to the wrapper as data.
const psScriptVar = "ZAKWAS_PS_SCRIPT"

// labelWidth is how much of a script an error message shows.
const labelWidth = 80

// cmd builds the invocation of script in c's shell. sh stays exactly
// `sh -c script`. PowerShell never reads a profile or prompts, and reports
// a failing last statement or native command by its exit code. cmd.exe takes
// one line, handed over verbatim with /s so its quote stripping applies.
func (m *Module) cmd(c config.Command, script string) (runner.Cmd, error) {
	sh := m.shellFor(c)
	win := m.goos() == platform.Windows
	run := runner.Cmd{Dir: m.Home}
	switch sh {
	case config.ShellSh, config.ShellBash:
		run.Name, run.Args = sh, []string{"-c", script}
	case config.ShellPowerShell, config.ShellPwsh:
		run.Name = sh
		run.Args = []string{"-NoProfile", "-NonInteractive"}
		if win {
			run.Args = append(run.Args, "-ExecutionPolicy", "Bypass")
		}
		run.Env = []string{"POWERSHELL_TELEMETRY_OPTOUT=1"}
		if plainPowerShell(script) {
			run.Args = append(run.Args, "-Command", plainScript(script))
		} else {
			run.Args = append(run.Args, "-Command", psWrapper)
			run.Env = append(run.Env, psScriptVar+"="+script)
		}
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
	if (run.Name != "sh" || c.Shell != "" || win) && !m.Runner.Installed(run.Name) {
		return run, fmt.Errorf("%s: shell %s is not installed (%s not found on PATH)", c.Name, sh, run.Name)
	}
	return run, tooLong(c.Name, sh, script, run, win)
}

// tooLong is the one-line error for a script the operating system would
// refuse to hand to its shell.
func tooLong(name, sh, script string, run runner.Cmd, win bool) error {
	switch {
	case sh == config.ShellCmd && win:
		if n := utf16Len(run.CmdLine); n > maxCmdLine {
			return fmt.Errorf("%s: the cmd line is %d characters, over the %d cmd.exe allows; put the script in a .cmd file and call it", name, n, maxCmdLine)
		}
	case (sh == config.ShellPwsh || sh == config.ShellPowerShell) && !plainPowerShell(script):
		n, limit, unit := len(script), maxUnixEnvVar, "bytes"
		if win {
			n, limit, unit = utf16Len(psScriptVar+"="+script), maxWindowsEnvVar, "characters"
		}
		if n > limit {
			return fmt.Errorf("%s: the %s script is %d %s, over the %d one environment variable holds; put it in a file and call it (& ./script.ps1)", name, sh, n, unit, limit)
		}
	case win:
		if n := windowsArgsLen(run); n > maxWindowsArgs {
			return fmt.Errorf("%s: the %s command line is %d characters, over the %d Windows allows; put the script in a file and call it", name, sh, n, maxWindowsArgs)
		}
	}
	return nil
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// windowsArgsLen is the length of the command line Go builds for run on
// Windows, following syscall.EscapeArg: an argument with a space, tab or
// quote is quoted, quotes are escaped, and backslashes are doubled only
// where they precede a quote or the closing one.
func windowsArgsLen(run runner.Cmd) int {
	n := utf16Len(run.Name)
	for _, a := range run.Args {
		n += 1 + escapedLen(a)
	}
	return n
}

func escapedLen(a string) int {
	if a == "" {
		return 2
	}
	n, slashes := 0, 0
	for _, r := range a {
		switch r {
		case '\\':
			slashes++
			n++
		case '"':
			n += slashes + 2
			slashes = 0
		default:
			n += utf16Len(string(r))
			slashes = 0
		}
	}
	if strings.ContainsAny(a, " \t") {
		n += 2 + slashes
	}
	return n
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

var (
	flowControl = regexp.MustCompile(`(?i)\b(return|break|continue)\b`)
	usingWord   = regexp.MustCompile(`(?i)\busing\b`)
)

// plainPowerShell reports a script that has to be the top-level text of the
// command, as in a plain `pwsh -Command`: `return`, `break` and `continue`
// end a script without letting anything run after it, and PowerShell reads
// the exit code from the status of the statement they follow. Such a script
// exits 0 or 1 (or its own `exit N`) and gets no native exit code; the
// words are looked for anywhere in the text, so a false match only costs
// that code.
func plainPowerShell(script string) bool { return flowControl.MatchString(script) }

// plainScript is a script run as the top-level command: after a line of
// settings on the same line as its first, so line numbers stay yours,
// unless it holds `using`, which must come first.
func plainScript(script string) string {
	if usingWord.MatchString(script) {
		return script
	}
	return psSettings + ";" + script
}

const psSettings = "$ProgressPreference='SilentlyContinue';try{$PSStyle.OutputRendering='PlainText'}catch{};" +
	"try{[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false);$OutputEncoding=[Console]::OutputEncoding}catch{}"

// psWrapper is the fixed PowerShell program that runs a script which does
// not use return, break or continue. The script arrives as data in the
// environment, so no text of it can close the wrapper. It is parsed first
// on its own, so a syntax error is a normal terminating error with your
// line numbers (and a leading `using` or block comment is where PowerShell
// wants it), then again with three statements appended that record `$?`,
// `$LASTEXITCODE` and the newest error. It runs in a child scope. Resolving
// a native command or script notes the newest error; when it is still the
// newest at the end, no cmdlet failed since the last native command, so a
// failure is that command's exit code, otherwise it is 1. $LASTEXITCODE
// itself is left alone, and a script that ends early some other way leaves
// the failure default. The console code page the wrapper changes is put
// back by the runner (KeepConsoleCP), which also covers `exit`, `throw`
// and a kill.
const psWrapper = psSettings + `
$__zakwas_src=$env:ZAKWAS_PS_SCRIPT
$env:ZAKWAS_PS_SCRIPT=$null
$__zakwas_ok=$false;$__zakwas_code=$null;$__zakwas_top=$null;$__zakwas_fresh=$false
$ExecutionContext.InvokeCommand.PostCommandLookupAction={param($n,$e);if($e.Command -and ($e.Command.CommandType -eq 'Application' -or $e.Command.CommandType -eq 'ExternalScript')){$global:__zakwas_top=@($Error)[0]}}
$null=[scriptblock]::Create($__zakwas_src)
$__zakwas_sb=[scriptblock]::Create($__zakwas_src+[char]10+'$global:__zakwas_ok=$?;$global:__zakwas_code=$LASTEXITCODE;$global:__zakwas_fresh=[object]::ReferenceEquals(@($Error)[0],$global:__zakwas_top)')
$__zakwas_src=$null
$Error.Clear()
& $__zakwas_sb
if($__zakwas_ok){exit 0}elseif($__zakwas_code -and $__zakwas_fresh){exit $__zakwas_code}else{exit 1}`
