package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
)

// eachPowerShell runs test for every PowerShell on PATH: pwsh anywhere, and
// Windows PowerShell on Windows. Without one the test skips, saying so.
func eachPowerShell(t *testing.T, test func(t *testing.T, shell string)) {
	t.Helper()
	found := false
	for _, shell := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		found = true
		t.Run(shell, func(t *testing.T) { test(t, shell) })
	}
	if !found {
		t.Skip("no PowerShell on PATH; the Windows job covers pwsh 7 and Windows PowerShell 5.1")
	}
}

func nativeExit(n int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("cmd /c exit %d", n)
	}
	return fmt.Sprintf("sh -c 'exit %d'", n)
}

const missingPath = `Get-Item -LiteralPath (Join-Path ([IO.Path]::GetTempPath()) 'zk-no-such-path')`

func runPowerShell(t *testing.T, shell, script string) runner.Result {
	t.Helper()
	m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: t.TempDir()}
	c, err := m.cmd(config.Command{Name: "n", Shell: shell}, script)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPowerShellExitCodes(t *testing.T) {
	tests := []struct {
		name, script string
		want         int
	}{
		{"success", "Write-Output ok", 0},
		{"a native failure, then a cmdlet that works", nativeExit(5) + "\nWrite-Output done", 0},
		{"a native failure as the last statement", "Write-Output start\n" + nativeExit(5), 5},
		{"a native failure after a cmdlet failure", missingPath + " -ErrorAction SilentlyContinue\n" + nativeExit(6), 6},
		{"a cmdlet failure after a native failure", nativeExit(5) + "\n" + missingPath, 1},
		{"a cmdlet failure after a native success", nativeExit(0) + "\n" + missingPath, 1},
		{"a cmdlet failure alone", missingPath, 1},
		{"a failure inside an if, after a native failure", nativeExit(5) + "\nif ($true) { " + missingPath + " }", 1},
		{"a native failure behind a pipeline stage that works", nativeExit(7) + " | Out-Null", 7},
		{"an expression error after a native failure", nativeExit(5) + "\n1/0", 1},
		{"exit", "exit 7", 7},
		{"exit after a native failure", nativeExit(5) + "\nexit 0", 0},
		{"throw", "throw 'boom'", 1},
		{"throw after a native failure", nativeExit(5) + "\nthrow 'boom'", 1},
		{"a syntax error", "if (", 1},
		{"false as the last statement", "$false", 0},
		{"a native failure, then false", nativeExit(5) + "\n$false", 0},
		{"return", "return", 0},
		{"a trailing comment", nativeExit(5) + " # fails", 5},
		{"the old code stays readable", nativeExit(3) + "\nWrite-Output x\nif ($LASTEXITCODE -eq 3) { exit 0 } else { exit 9 }", 0},
		{"a script that reads LASTEXITCODE as null first", "if ($null -eq $LASTEXITCODE -or $LASTEXITCODE -eq 0) { exit 0 } else { exit 9 }", 0},
		{"a function that fails leaves $? true, as in plain pwsh", nativeExit(5) + "\nfunction f { " + missingPath + " }\nf", 0},
	}
	eachPowerShell(t, func(t *testing.T, shell string) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				res := runPowerShell(t, shell, tt.script)
				if res.ExitCode != tt.want {
					t.Errorf("exit %d, want %d\nscript:\n%s\nstdout: %s\nstderr: %s", res.ExitCode, tt.want, tt.script, res.Stdout, res.Stderr)
				}
			})
		}
	})
}

func TestPowerShellKeepsLeadingUsingStatements(t *testing.T) {
	script := "# a note\nusing namespace System.Text\n\n[StringBuilder]::new() | Out-Null\nNew-Item -ItemType File -Path (Join-Path $env:ZK_USING_DIR ok) | Out-Null"
	eachPowerShell(t, func(t *testing.T, shell string) {
		dir := t.TempDir()
		t.Setenv("ZK_USING_DIR", dir)

		res := runPowerShell(t, shell, script)

		if res.ExitCode != 0 {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", res.ExitCode, res.Stdout, res.Stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "ok")); err != nil {
			t.Errorf("the script did not run: %v", err)
		}
	})
}

func TestPowerShellWritesUTF8AndQuotes(t *testing.T) {
	eachPowerShell(t, func(t *testing.T, shell string) {
		res := runPowerShell(t, shell, `Write-Output 'zażółć gęślą "q" it''s'`)

		if res.ExitCode != 0 {
			t.Fatalf("exit %d\n%s", res.ExitCode, res.Stderr)
		}
		if got := strings.TrimSpace(res.Stdout); got != `zażółć gęślą "q" it's` {
			t.Errorf("stdout = %q; non-ASCII text and quotes must arrive as written", got)
		}
	})
}

func TestPowerShellErrorsAreWrittenWithoutColour(t *testing.T) {
	eachPowerShell(t, func(t *testing.T, shell string) {
		res := runPowerShell(t, shell, missingPath)

		if res.ExitCode != 1 || res.Stderr == "" || strings.Contains(res.Stderr, "\x1b") {
			t.Errorf("exit %d, stderr %q; want an error without escape sequences", res.ExitCode, res.Stderr)
		}
	})
}

func TestPowerShellNeverPrompts(t *testing.T) {
	eachPowerShell(t, func(t *testing.T, shell string) {
		res := runPowerShell(t, shell, `Read-Host 'name'`)

		if res.ExitCode == 0 {
			t.Errorf("exit 0; a prompt must fail, not wait")
		}
	})
}

func TestPowerShellRunsTheScriptInTheCallersScope(t *testing.T) {
	eachPowerShell(t, func(t *testing.T, shell string) {
		res := runPowerShell(t, shell, "$x = 41\nfunction f { $x + 1 }\nWrite-Output (f)")

		if strings.TrimSpace(res.Stdout) != "42" {
			t.Errorf("stdout = %q", res.Stdout)
		}
	})
}
