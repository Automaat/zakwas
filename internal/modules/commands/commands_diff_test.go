package commands

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
)

type outcome struct {
	code   int
	stdout string
	files  map[string]string
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// normalize drops what differs by design: line ending style and the colour
// codes plain pwsh adds to warnings, which the wrapper turns off.
func normalize(s string) string {
	return strings.TrimRight(ansi.ReplaceAllString(strings.ReplaceAll(s, "\r\n", "\n"), ""), " \n")
}

func listFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = normalize(string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// runPlain is the reference: the script as the text of a plain
// `pwsh -NoProfile -NonInteractive -Command`.
func runPlain(t *testing.T, shell, script string) outcome {
	t.Helper()
	dir := t.TempDir()
	args := []string{"-NoProfile", "-NonInteractive"}
	if runtime.GOOS == "windows" {
		args = append(args, "-ExecutionPolicy", "Bypass")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, append(args, "-Command", script)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ZK_DIR="+dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return outcome{code, normalize(out.String()), listFiles(t, dir)}
}

func runOurs(t *testing.T, shell, script string) outcome {
	t.Helper()
	dir := t.TempDir()
	m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: dir}
	c, err := m.cmd(config.Command{Name: "n", Shell: shell}, script)
	if err != nil {
		t.Fatal(err)
	}
	c.Env = append(c.Env, "ZK_DIR="+dir)
	res, err := m.Runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return outcome{res.ExitCode, normalize(res.Stdout), listFiles(t, dir)}
}

const (
	zkFile   = `(Join-Path $env:ZK_DIR 'a.txt')`
	zkMarker = `Set-Content -LiteralPath ` + zkFile + ` -Value done`
)

// TestScriptsRunLikePlainPowerShell runs each script as the text of a plain
// `pwsh -Command` and through zakwas, and demands the same exit code, the
// same stdout and the same files. A script that fails in the plain run must
// never succeed here, and one that succeeds must not fail. want pins the
// code where docs/config.md states it (-1: only equality is checked).
func TestScriptsRunLikePlainPowerShell(t *testing.T) {
	const noSuch = missingPath
	passOn := "\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }"
	tests := []struct {
		name, script string
		want         int
	}{
		{"output", "Write-Output hi", 0},
		{"nothing", "$null", 0},
		{"a comment only", "# nothing to do", 0},
		{"a block comment only", "<# nothing #>", 0},
		{"a block comment, then a command", "<# note #> Write-Output y", 0},
		{"a bare exit", "exit", -1},
		{"a leading minus", "-1", 0},
		{"a leading splat array", "@(1, 2)", 0},
		{"a leading call operator", "& { 'x' }", 0},
		{"native success", nativeExit(0), 0},
		{"native failure last", "Write-Output start\n" + nativeExit(5), 1},
		{"native failure passed on", nativeExit(5) + passOn, 5},
		{"native failure then a good cmdlet", nativeExit(5) + "\nWrite-Output done", 0},
		{"cmdlet failure", noSuch, 1},
		{"cmdlet failure after a native failure", nativeExit(5) + "\n" + noSuch, 1},
		{"cmdlet failure after a native success", nativeExit(0) + "\n" + noSuch, 1},
		{"native failure after a cmdlet failure", noSuch + " -ErrorAction SilentlyContinue\n" + nativeExit(6), 1},
		{"ignored cmdlet failure after a native failure", nativeExit(3) + "\n" + noSuch + " -ErrorAction Ignore", -1},
		{"silenced cmdlet failure alone", noSuch + " -ErrorAction SilentlyContinue", 1},
		{"exit", "exit 7", 7},
		{"exit 0 after failures", nativeExit(3) + "\n" + noSuch + " -EA SilentlyContinue\nexit 0", 0},
		{"throw", "throw 'x'", 1},
		{"throw in a function", "function f { throw 'x' }\nf", 1},
		{"syntax error", "if (", 1},
		{"unbalanced braces closing a wrapper", "} ; exit 0 ; . {", 1},
		{"unbalanced brace opening", "if ($true) {", 1},
		{"unterminated string", "Write-Output 'abc", 1},
		{"unterminated here-string", "Write-Output @'\nabc", 1},
		{"unterminated block comment", "<# open\nWrite-Output hi", 1},
		{"false last", "$false", 0},
		{"native failure then false", nativeExit(5) + "\n$false", 0},
		{"return", "return", 0},
		{"error then return", "Write-Error 'missing'; return", 1},
		{"cmdlet failure then return", noSuch + "\nreturn", 1},
		{"native failure then return in an if", nativeExit(3) + "\nif ($true) { return }", -1},
		{"native failure then break", nativeExit(3) + "\nbreak", -1},
		{"native failure then continue", nativeExit(3) + "\ncontinue", -1},
		{"a runtime-built break", "Invoke-Command { iex ('br' + 'eak') }", 1},
		{"a runtime-built continue", nativeExit(3) + "\nInvoke-Command { iex ('con' + 'tinue') }", -1},
		{"the check idiom with return", "if (-not (Test-Path (Join-Path $env:ZK_DIR 'nope'))) { Write-Error 'missing'; return }", 1},
		{"the check idiom with exit", "if (-not (Test-Path (Join-Path $env:ZK_DIR 'nope'))) { exit 1 }", 1},
		{"break inside a loop", "for ($i = 0; $i -lt 3; $i++) { if ($i -eq 1) { break } }", 0},
		{"continue inside a loop", "foreach ($i in 1..3) { if ($i -eq 2) { continue }; Write-Output $i }", 0},
		{"a function that returns", "function f { return 5 }\nWrite-Output (f)", 0},
		{"the word return in a string", "Write-Output 'return to sender'", 0},
		{"LASTEXITCODE set to zero", "$LASTEXITCODE = 0\n" + nativeExit(2) + "\nif ($LASTEXITCODE -ne 0) { exit 1 }", 1},
		{"LASTEXITCODE set to null", "$LASTEXITCODE = $null\nWrite-Output ok", 0},
		{"global LASTEXITCODE set to null", "$global:LASTEXITCODE = $null\nWrite-Output ok", 0},
		{"LASTEXITCODE null under Stop", "$ErrorActionPreference = 'Stop'\n$LASTEXITCODE = $null\nWrite-Output ok", 0},
		{"global LASTEXITCODE null under Stop", "$ErrorActionPreference = 'Stop'\n$global:LASTEXITCODE = $null\nWrite-Output ok", 0},
		{"a typed LASTEXITCODE", "[int]$LASTEXITCODE = 0\n" + nativeExit(2) + "\nif ($LASTEXITCODE -ne 0) { exit 1 }", 1},
		{"Set-Variable LASTEXITCODE", "Set-Variable LASTEXITCODE 0\n" + nativeExit(2) + "\nif ($LASTEXITCODE -ne 0) { exit 1 }", 1},
		{"a local LASTEXITCODE", "$local:LASTEXITCODE = 0\n" + nativeExit(2) + "\nif ($LASTEXITCODE -ne 0) { exit 1 }", 1},
		{"Error replaced", "$Error = @()\n" + noSuch, 1},
		{"exit with a non-numeric LASTEXITCODE", "$LASTEXITCODE = 'abc'\nexit $LASTEXITCODE", -1},
		{"exit with a multiple of 256", "Set-Content -LiteralPath (Join-Path $env:ZK_DIR 'h.ps1') -Value 'exit 256'\n& (Join-Path $env:ZK_DIR 'h.ps1')\nexit $LASTEXITCODE", -1},
		{"exit with an array", "exit @(1, 2)", -1},
		{"cmdlet failure under Stop", "$ErrorActionPreference = 'Stop'\n" + noSuch, 1},
		{"native failure under Stop", "$ErrorActionPreference = 'Stop'\n" + nativeExit(2), 1},
		{"Stop with strict mode and no native command", "$ErrorActionPreference = 'Stop'\nSet-StrictMode -Version Latest\nWrite-Output ok", 0},
		{"LASTEXITCODE stays readable", nativeExit(3) + "\nWrite-Output x\nif ($LASTEXITCODE -eq 3) { exit 0 } else { exit 9 }", 0},
		{"LASTEXITCODE unset at the start", "if ($null -eq $LASTEXITCODE -or $LASTEXITCODE -eq 0) { exit 0 } else { exit 9 }", 0},
		{"block comment before using", "<# a note #>\nusing namespace System.Text\n[StringBuilder]::new() | Out-Null\n" + zkMarker, 0},
		{"multi-line block comment before using", "<#\n a note\n#>\n\n# more\nusing namespace System.Text\n[StringBuilder]::new() | Out-Null\n" + zkMarker, 0},
		{"only a using statement", "using namespace System.Text", 0},
		{"using and a statement on one line", "using namespace System.Text; [StringBuilder]::new() | Out-Null; " + zkMarker, 0},
		{"using with a tab", "using\tnamespace System.Text\n[StringBuilder]::new() | Out-Null", 0},
		{"using with return", "using namespace System.Text\n[StringBuilder]::new() | Out-Null\nreturn", 0},
		{"using and a failure", "using namespace System.Text\n" + noSuch, 1},
		{"exit in finally", "try { 'x' } finally { exit 4 }", 4},
		{"exit in a catch", "try { throw 'x' } catch { exit 6 }", 6},
		{"a caught throw", "try { throw 'x' } catch { 'caught' }", 0},
		{"a trap", "trap { 'trapped'; continue }\nthrow 'x'", -1},
		{"a prompt", "Read-Host 'name'", 1},
		{"a secure prompt", "Read-Host -AsSecureString 'pw'", 1},
		{"large output", "1..20000 | ForEach-Object { \"line $_\" }", 0},
		{"one huge line", "Write-Output ('x' * 1000000)", 0},
		{"stderr noise", "Write-Error 'x'", 1},
		{"a warning", "Write-Warning 'w'", 0},
		{"host output", "Write-Host 'host'", 0},
		{"non-ASCII text", "Write-Output 'zażółć gęślą jaźń'", 0},
		{"a side effect before a native failure", zkMarker + "\n" + nativeExit(2), 1},
		{"no script path", "if ($PSCommandPath) { exit 5 }", 0},
		{"a native pipeline", nativeExit(7) + " | Out-Null", 1},
		{"division by zero", "1/0", 1},
		{"division by zero then more", "1/0\nWrite-Output after", 0},
		{"a failed conversion", "[int]'x'", 1},
		{"an assignment from a failing cmdlet", "$x = " + strings.TrimSpace(missingPath) + "\nWrite-Output after", 0},
		{"strict mode", "Set-StrictMode -Version Latest\n$undefined", 1},
		{"a script block exit", "& { exit 3 }", 3},
		{"exit in an if", "if ($true) { exit 2 }", 2},
		{"a script ending in a comment", "Write-Output hi # end", 0},
		{"CRLF line endings", "Write-Output one\r\nWrite-Output two\r\n", 0},
		{"variables and functions", "$x = 41\nfunction f { $x + 1 }\nWrite-Output (f)", 0},
		{"a here-string", "$t = @'\nline one\nline two\n'@\nWrite-Output $t", 0},
		{"a pipeline failing in the middle", "1..3 | ForEach-Object { if ($_ -eq 2) { Write-Error 'two' } else { $_ } }", 0},
		{"a failing native command in a function", "function f { " + nativeExit(4) + " }\nf", -1},
	}
	eachPowerShell(t, func(t *testing.T, shell string) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				plain, ours := runPlain(t, shell, tt.script), runOurs(t, shell, tt.script)

				if plain.code != 0 && ours.code == 0 {
					t.Errorf("a failure in plain pwsh (exit %d) is a success through zakwas", plain.code)
				}
				if plain.code != ours.code {
					t.Errorf("exit codes differ: plain %d, zakwas %d", plain.code, ours.code)
				}
				if tt.want >= 0 && ours.code != tt.want {
					t.Errorf("exit %d, want %d", ours.code, tt.want)
				}
				if plain.stdout != ours.stdout {
					t.Errorf("stdout differs:\nplain: %.200q\nzakwas: %.200q", plain.stdout, ours.stdout)
				}
				if len(plain.files) != len(ours.files) {
					t.Errorf("files differ: plain %v, zakwas %v", plain.files, ours.files)
				}
				for name, content := range plain.files {
					if ours.files[name] != content {
						t.Errorf("file %s: plain %q, zakwas %q", name, content, ours.files[name])
					}
				}
			})
		}
	})
}
