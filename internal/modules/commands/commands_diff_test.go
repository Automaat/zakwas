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

func runWrapped(t *testing.T, shell, script string) outcome {
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

// TestWrapperAgainstPlainPowerShell runs each script as the text of a plain
// `pwsh -Command` and through the wrapper. A script that fails in the plain
// run must never exit 0 through the wrapper; one that succeeds must not
// fail; stdout and files written must match. want is the wrapper's exit code
// where the docs promise one (a native command's own), exact says both runs
// must give the same code.
func TestWrapperAgainstPlainPowerShell(t *testing.T) {
	const noSuch = missingPath
	tests := []struct {
		name, script string
		want         int
		exact        bool
	}{
		{"output", "Write-Output hi", -1, true},
		{"nothing", "$null", -1, true},
		{"native success", nativeExit(0), -1, true},
		{"native failure last", "Write-Output start\n" + nativeExit(5), 5, false},
		{"native failure then a good cmdlet", nativeExit(5) + "\nWrite-Output done", 0, true},
		{"cmdlet failure", noSuch, 1, true},
		{"cmdlet failure after a native failure", nativeExit(5) + "\n" + noSuch, 1, true},
		{"cmdlet failure after a native success", nativeExit(0) + "\n" + noSuch, 1, true},
		{"native failure after a cmdlet failure", noSuch + " -ErrorAction SilentlyContinue\n" + nativeExit(6), 6, false},
		{"ignored cmdlet failure after a native failure", nativeExit(3) + "\n" + noSuch + " -ErrorAction Ignore", -1, false},
		{"silenced cmdlet failure alone", noSuch + " -ErrorAction SilentlyContinue", 1, true},
		{"exit", "exit 7", 7, true},
		{"exit 0 after failures", nativeExit(3) + "\n" + noSuch + " -EA SilentlyContinue\nexit 0", 0, true},
		{"throw", "throw 'x'", 1, true},
		{"throw in a function", "function f { throw 'x' }\nf", 1, true},
		{"syntax error", "if (", 1, true},
		{"unbalanced braces closing the wrapper", "} ; exit 0 ; . {", 1, true},
		{"unbalanced brace opening", "if ($true) {", 1, true},
		{"unterminated string", "Write-Output 'abc", 1, true},
		{"unterminated here-string", "Write-Output @'\nabc", 1, true},
		{"unterminated block comment", "<# open\nWrite-Output hi", 1, true},
		{"false last", "$false", 0, true},
		{"native failure then false", nativeExit(5) + "\n$false", 0, true},
		{"true last", "$true", 0, true},
		{"return", "return", 0, true},
		{"error then return", "Write-Error 'missing'; return", 1, true},
		{"cmdlet failure then return", noSuch + "\nreturn", 1, true},
		{"native failure then return in an if", nativeExit(3) + "\nif ($true) { return }", -1, true},
		{"native failure then break", nativeExit(3) + "\nbreak", -1, true},
		{"native failure then continue", nativeExit(3) + "\ncontinue", -1, true},
		{"the check idiom with return", "if (-not (Test-Path (Join-Path $env:ZK_DIR 'nope'))) { Write-Error 'missing'; return }", 1, true},
		{"the check idiom with exit", "if (-not (Test-Path (Join-Path $env:ZK_DIR 'nope'))) { exit 1 }", 1, true},
		{"break inside a loop", "for ($i = 0; $i -lt 3; $i++) { if ($i -eq 1) { break } }", 0, true},
		{"continue inside a loop", "foreach ($i in 1..3) { if ($i -eq 2) { continue }; Write-Output $i }", 0, true},
		{"a function that returns", "function f { return 5 }\nWrite-Output (f)", 0, true},
		{"the word return in a string", "Write-Output 'return to sender'", 0, true},
		{"LASTEXITCODE set to null", "$LASTEXITCODE = $null\nWrite-Output ok", 0, true},
		{"global LASTEXITCODE set to null", "$global:LASTEXITCODE = $null\nWrite-Output ok", 0, true},
		{"LASTEXITCODE null under Stop", "$ErrorActionPreference = 'Stop'\n$LASTEXITCODE = $null\nWrite-Output ok", 0, true},
		{"global LASTEXITCODE null under Stop", "$ErrorActionPreference = 'Stop'\n$global:LASTEXITCODE = $null\nWrite-Output ok", 0, true},
		{"cmdlet failure under Stop", "$ErrorActionPreference = 'Stop'\n" + noSuch, 1, true},
		{"native failure under Stop", "$ErrorActionPreference = 'Stop'\n" + nativeExit(2), 2, false},
		{"LASTEXITCODE stays readable", nativeExit(3) + "\nWrite-Output x\nif ($LASTEXITCODE -eq 3) { exit 0 } else { exit 9 }", 0, true},
		{"LASTEXITCODE unset at the start", "if ($null -eq $LASTEXITCODE -or $LASTEXITCODE -eq 0) { exit 0 } else { exit 9 }", 0, true},
		{"block comment before using", "<# a note #>\nusing namespace System.Text\n[StringBuilder]::new() | Out-Null\n" + zkMarker, 0, true},
		{"multi-line block comment before using", "<#\n a note\n#>\n\n# more\nusing namespace System.Text\n[StringBuilder]::new() | Out-Null\n" + zkMarker, 0, true},
		{"only a using statement", "using namespace System.Text", 0, true},
		{"using and a statement on one line", "using namespace System.Text; [StringBuilder]::new() | Out-Null; " + zkMarker, 0, true},
		{"using with a tab", "using\tnamespace System.Text\n[StringBuilder]::new() | Out-Null", 0, true},
		{"using with return", "using namespace System.Text\n[StringBuilder]::new() | Out-Null\nreturn", 0, true},
		{"using and a failure", "using namespace System.Text\n" + noSuch, 1, true},
		{"exit in finally", "try { 'x' } finally { exit 4 }", 4, true},
		{"exit in a catch", "try { throw 'x' } catch { exit 6 }", 6, true},
		{"a caught throw", "try { throw 'x' } catch { 'caught' }", 0, true},
		{"a trap", "trap { 'trapped'; continue }\nthrow 'x'", -1, true},
		{"a prompt", "Read-Host 'name'", 1, true},
		{"a secure prompt", "Read-Host -AsSecureString 'pw'", 1, true},
		{"large output", "1..20000 | ForEach-Object { \"line $_\" }", 0, true},
		{"one huge line", "Write-Output ('x' * 1000000)", 0, true},
		{"stderr noise", "Write-Error 'x'", 1, true},
		{"a warning", "Write-Warning 'w'", 0, true},
		{"host output", "Write-Host 'host'", 0, true},
		{"non-ASCII text", "Write-Output 'zażółć gęślą jaźń'", 0, true},
		{"a side effect before a native failure", zkMarker + "\n" + nativeExit(2), 2, false},
		{"the script variable is not exposed", "if ($env:ZAKWAS_PS_SCRIPT) { exit 5 }", 0, true},
		{"no script path", "if ($PSCommandPath) { exit 5 }", 0, true},
		{"a native pipeline", nativeExit(7) + " | Out-Null", 7, false},
		{"division by zero", "1/0", 1, true},
		{"division by zero then more", "1/0\nWrite-Output after", 0, true},
		{"a failed conversion", "[int]'x'", 1, true},
		{"an assignment from a failing cmdlet", "$x = " + strings.TrimSpace(missingPath) + "\nWrite-Output after", 0, true},
		{"strict mode", "Set-StrictMode -Version Latest\n$undefined", 1, true},
		{"a script block exit", "& { exit 3 }", 3, true},
		{"exit in an if", "if ($true) { exit 2 }", 2, true},
		{"a script ending in a comment", "Write-Output hi # end", 0, true},
		{"CRLF line endings", "Write-Output one\r\nWrite-Output two\r\n", 0, true},
		{"variables and functions", "$x = 41\nfunction f { $x + 1 }\nWrite-Output (f)", 0, true},
		{"a here-string", "$t = @'\nline one\nline two\n'@\nWrite-Output $t", 0, true},
		{"a pipeline failing in the middle", "1..3 | ForEach-Object { if ($_ -eq 2) { Write-Error 'two' } else { $_ } }", 0, true},
		{"a failing native command in a function", "function f { " + nativeExit(4) + " }\nf", -1, false},
	}
	eachPowerShell(t, func(t *testing.T, shell string) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				plain, wrapped := runPlain(t, shell, tt.script), runWrapped(t, shell, tt.script)

				if plain.code != 0 && wrapped.code == 0 {
					t.Errorf("a failure in plain pwsh (exit %d) is a success through the wrapper", plain.code)
				}
				if (plain.code == 0) != (wrapped.code == 0) {
					t.Errorf("exit classes differ: plain %d, wrapped %d", plain.code, wrapped.code)
				}
				if tt.exact && plain.code != wrapped.code {
					t.Errorf("exit codes differ: plain %d, wrapped %d", plain.code, wrapped.code)
				}
				if tt.want >= 0 && wrapped.code != tt.want {
					t.Errorf("wrapped exit %d, want %d", wrapped.code, tt.want)
				}
				if plain.stdout != wrapped.stdout {
					t.Errorf("stdout differs:\nplain:   %.200q\nwrapped: %.200q", plain.stdout, wrapped.stdout)
				}
				if len(plain.files) != len(wrapped.files) {
					t.Errorf("files differ: plain %v, wrapped %v", plain.files, wrapped.files)
				}
				for name, content := range plain.files {
					if wrapped.files[name] != content {
						t.Errorf("file %s: plain %q, wrapped %q", name, content, wrapped.files[name])
					}
				}
			})
		}
	})
}
