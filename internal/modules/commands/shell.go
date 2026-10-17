package commands

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
)

// Windows hands a process at most 32,766 characters of command line, and
// cmd.exe itself stops at 8,191. Linux refuses one argument of 131,072 bytes
// or more, the terminating NUL included.
const (
	maxWindowsArgs = 32766
	maxCmdLine     = 8191
	maxLinuxArg    = 131071
)

// labelWidth is how much of a script an error message shows.
const labelWidth = 80

// utf8CodePage is the console output code page PowerShell is started with
// on Windows, which it takes for its output encoding.
const utf8CodePage = 65001

// cmd builds the invocation of script in c's shell. sh stays exactly
// `sh -c script`. PowerShell never reads a profile or prompts and gets the
// script untouched as the text of -Command; only its environment and the
// console code page differ from a plain run. cmd.exe takes one line, handed
// over verbatim with /s so its quote stripping applies.
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
		run.Args = append(run.Args, "-Command", script)
		run.Env = []string{"POWERSHELL_TELEMETRY_OPTOUT=1", "NO_COLOR=1"}
		run.Label = sh + " -Command " + shorten(script)
		run.ConsoleCP = utf8CodePage
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
	return run, tooLong(c.Name, sh, run, m.goos())
}

// tooLong is the one-line error for a script the operating system would
// refuse to hand to its shell.
func tooLong(name, sh string, run runner.Cmd, goos string) error {
	switch goos {
	case platform.Windows:
		if sh == config.ShellCmd {
			if n := utf16Len(run.CmdLine); n > maxCmdLine {
				return fmt.Errorf("%s: the cmd line is %d characters, over the %d cmd.exe allows; put the script in a .cmd file and call it", name, n, maxCmdLine)
			}
		} else if n := windowsArgsLen(run); n > maxWindowsArgs {
			return fmt.Errorf("%s: the %s command line is %d characters, over the %d Windows allows; put the script in a file and call it", name, sh, n, maxWindowsArgs)
		}
	case platform.Linux:
		for _, a := range run.Args {
			if len(a) > maxLinuxArg {
				return fmt.Errorf("%s: the %s script is %d bytes, over the %d Linux allows in one argument; put it in a file and call it", name, sh, len(a), maxLinuxArg)
			}
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
