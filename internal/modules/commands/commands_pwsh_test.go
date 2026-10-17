package commands

import (
	"context"
	"fmt"
	"os/exec"
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

// TestPowerShellExitCodes pins what docs/config.md promises: the exit code
// of a plain pwsh -Command, where a failing last statement is 1 and a native
// command's own code only travels when the script says so.
func TestPowerShellExitCodes(t *testing.T) {
	passOn := "\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }"
	tests := []struct {
		name, script string
		want         int
	}{
		{"success", "Write-Output ok", 0},
		{"a native failure, then a cmdlet that works", nativeExit(5) + "\nWrite-Output done", 0},
		{"a native failure as the last statement is 1", "Write-Output start\n" + nativeExit(5), 1},
		{"a native failure passed on", nativeExit(5) + passOn, 5},
		{"a native success passed on", nativeExit(0) + passOn, 0},
		{"a cmdlet failure after a native failure", nativeExit(5) + "\n" + missingPath, 1},
		{"a cmdlet failure", missingPath, 1},
		{"exit", "exit 7", 7},
		{"throw", "throw 'boom'", 1},
		{"a syntax error", "if (", 1},
		{"false as the last statement", "$false", 0},
		{"return", "return", 0},
		{"LASTEXITCODE reset by the script", "$LASTEXITCODE = 0\n" + nativeExit(2) + "\nif ($LASTEXITCODE -ne 0) { exit 1 }", 1},
		{"LASTEXITCODE set to null under Stop", "$ErrorActionPreference = 'Stop'\n$LASTEXITCODE = $null\nWrite-Output ok", 0},
		{"Stop with strict mode and no native command", "$ErrorActionPreference = 'Stop'\nSet-StrictMode -Version Latest\nWrite-Output ok", 0},
		{"a runtime-built break", "Invoke-Command { iex ('br' + 'eak') }", 1},
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
