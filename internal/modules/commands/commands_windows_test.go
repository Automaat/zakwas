//go:build windows

package commands

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
)

// scripts are one task per shell, written in that shell's own language, as a
// user's zakwas.yaml would have them.
type scripts struct {
	pass, fail, code3, exists, write, relative string
}

var realShells = map[string]scripts{
	"pwsh":       powerShellScripts,
	"powershell": powerShellScripts,
	"cmd": {
		pass:     `exit /b 0`,
		fail:     `exit /b 1`,
		code3:    `exit /b 3`,
		exists:   `if exist "%ZK_DIR%\a b.txt" (exit /b 0) else (exit /b 1)`,
		write:    `echo streamed& echo "it's" > "%ZK_DIR%\a b.txt"`,
		relative: `echo x > rel.txt`,
	},
}

var powerShellScripts = scripts{
	pass:     `exit 0`,
	fail:     `exit 1`,
	code3:    `cmd /c exit 3`,
	exists:   `if (Test-Path -LiteralPath "$env:ZK_DIR\a b.txt") { exit 0 } else { exit 1 }`,
	write:    `Write-Output streamed; Set-Content -LiteralPath "$env:ZK_DIR\a b.txt" -Value 'it''s "quoted"'`,
	relative: `Set-Content -Path rel.txt -Value x`,
}

func eachShell(t *testing.T, test func(t *testing.T, shell string, s scripts)) {
	t.Helper()
	for shell, s := range realShells {
		bin := shell
		if shell == "cmd" {
			bin = "cmd.exe"
		}
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(bin); err != nil {
				t.Skipf("%s is not on PATH here: %v", bin, err)
			}
			t.Setenv("ZK_DIR", t.TempDir())
			test(t, shell, s)
		})
	}
}

func realModule(shell string, out *bytes.Buffer, cmds ...config.Command) *Module {
	for i := range cmds {
		cmds[i].Shell = shell
	}
	return &Module{
		Commands: cmds,
		Home:     os.Getenv("ZK_DIR"),
		Runner:   &runner.Exec{Stdout: out, Stderr: out, NoStdin: true, Detached: true, Grace: 2 * time.Second},
	}
}

func apply(t *testing.T, m *Module) ([]engine.Change, error) {
	t.Helper()
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return changes, enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
}

func TestRealShellsConvergeAndQuoteSpaces(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string, s scripts) {
		var out bytes.Buffer
		m := realModule(shell, &out, config.Command{Name: "file", Check: s.exists, Run: s.write})

		changes, err := apply(t, m)

		if err != nil || len(changes) != 1 {
			t.Fatalf("changes = %d, err = %v\n%s", len(changes), err, out.String())
		}
		if !strings.Contains(out.String(), "streamed") {
			t.Errorf("the run step's output did not stream: %q", out.String())
		}
		data, err := os.ReadFile(filepath.Join(os.Getenv("ZK_DIR"), "a b.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(data)); !strings.Contains(got, `it's`) {
			t.Errorf("file = %q", got)
		}
		again, err := m.Plan(context.Background())
		if err != nil || len(again) != 0 {
			t.Errorf("a second plan wants %v, err = %v", again, err)
		}
	})
}

func TestRealShellsRunInHome(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string, s scripts) {
		var out bytes.Buffer
		m := realModule(shell, &out, config.Command{Name: "rel", Check: `if exist rel.txt (exit /b 0) else (exit /b 1)`, Run: s.relative})
		if shell != "cmd" {
			m.Commands[0].Check = `if (Test-Path -LiteralPath rel.txt) { exit 0 } else { exit 1 }`
		}

		if _, err := apply(t, m); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("ZK_DIR"), "rel.txt")); err != nil {
			t.Errorf("a relative path did not land in the home directory: %v", err)
		}
	})
}

func TestRealShellsPropagateExitCodes(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string, s scripts) {
		m := realModule(shell, &bytes.Buffer{})
		tests := []struct {
			name, script string
			want         int
		}{
			{"success", s.pass, 0},
			{"failure", s.fail, 1},
			{"a native command's own code", s.code3, 3},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				c, err := m.cmd(config.Command{Name: "n", Shell: shell}, tt.script)
				if err != nil {
					t.Fatal(err)
				}
				res, err := m.Runner.Run(context.Background(), c)
				if err != nil || res.ExitCode != tt.want {
					t.Errorf("exit %d, err %v, want %d\nstdout: %s\nstderr: %s", res.ExitCode, err, tt.want, res.Stdout, res.Stderr)
				}
			})
		}
	})
}

func TestPowerShellExitsLikeShDoes(t *testing.T) {
	tests := []struct {
		name, script string
		want         int
	}{
		{"a later success hides an earlier native failure", "cmd /c exit 3\nWrite-Output done", 0},
		{"a failing last native command", "Write-Output start\ncmd /c exit 4", 4},
		{"a failing cmdlet", `Get-Item -LiteralPath C:\zakwas-no-such-path -ErrorAction SilentlyContinue; Get-Item -LiteralPath C:\zakwas-no-such-path`, 1},
		{"an explicit exit", "exit 7", 7},
		{"a trailing comment", "cmd /c exit 5 # fails", 5},
		{"a failing check written as a condition", `if (-not (Test-Path C:\zakwas-no-such-path)) { exit 1 }`, 1},
	}
	for _, shell := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(shell); err != nil {
			t.Logf("%s is not on PATH here", shell)
			continue
		}
		for _, tt := range tests {
			t.Run(shell+"/"+tt.name, func(t *testing.T) {
				m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: t.TempDir()}
				c, err := m.cmd(config.Command{Name: "n", Shell: shell}, tt.script)
				if err != nil {
					t.Fatal(err)
				}
				res, err := m.Runner.Run(context.Background(), c)
				if err != nil || res.ExitCode != tt.want {
					t.Errorf("exit %d, err %v, want %d\nstdout: %s\nstderr: %s", res.ExitCode, err, tt.want, res.Stdout, res.Stderr)
				}
			})
		}
	}
}

func TestPowerShellWritesUTF8AndQuotes(t *testing.T) {
	for _, shell := range []string{"pwsh", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not on PATH here: %v", shell, err)
			}
			m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: t.TempDir()}
			c, err := m.cmd(config.Command{Name: "n", Shell: shell}, `Write-Output "zażółć 'gęślą' $env:USERNAME_ZK"`)
			if err != nil {
				t.Fatal(err)
			}

			res, err := m.Runner.Run(context.Background(), c)

			if err != nil || res.ExitCode != 0 {
				t.Fatalf("exit %d, err %v\n%s", res.ExitCode, err, res.Stderr)
			}
			if got := strings.TrimSpace(res.Stdout); got != "zażółć 'gęślą'" {
				t.Errorf("stdout = %q; non-ASCII text and quotes must arrive as written", got)
			}
		})
	}
}

func TestPowerShellNeverPrompts(t *testing.T) {
	for _, shell := range []string{"pwsh", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not on PATH here: %v", shell, err)
			}
			m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: t.TempDir()}
			c, err := m.cmd(config.Command{Name: "n", Shell: shell}, `Read-Host 'name'`)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			res, err := m.Runner.Run(ctx, c)

			if err != nil || res.ExitCode == 0 {
				t.Errorf("exit %d, err %v; a prompt must fail, not wait", res.ExitCode, err)
			}
		})
	}
}

func TestHungCheckIsKilledOnWindows(t *testing.T) {
	hangs := map[string]string{
		"pwsh":       `Start-Sleep -Seconds 60`,
		"powershell": `Start-Sleep -Seconds 60`,
		"cmd":        `ping -n 60 127.0.0.1 > nul`,
	}
	eachShell(t, func(t *testing.T, shell string, _ scripts) {
		defer func(d time.Duration) { CheckTimeout = d }(CheckTimeout)
		CheckTimeout = 1500 * time.Millisecond
		m := realModule(shell, &bytes.Buffer{}, config.Command{Name: "hang", Check: hangs[shell], Run: "exit 0"})
		start := time.Now()

		_, err := m.Plan(context.Background())

		if err == nil || !strings.Contains(err.Error(), "hang: check timed out") {
			t.Errorf("err = %v", err)
		}
		if d := time.Since(start); d > 15*time.Second {
			t.Errorf("plan took %s; the shell and its child must be killed at the timeout", d)
		}
	})
}

func TestRealShellsStopAtTheFirstFailure(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string, s scripts) {
		var out bytes.Buffer
		m := realModule(shell, &out,
			config.Command{Name: "first", Check: s.fail, Run: s.code3},
			config.Command{Name: "second", Check: s.fail, Run: s.write},
		)

		_, err := apply(t, m)

		if err == nil || !strings.Contains(err.Error(), "exit 3") {
			t.Errorf("err = %v, want the first run's exit code", err)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("ZK_DIR"), "a b.txt")); err == nil {
			t.Error("the second entry ran after the first failed")
		}
	})
}

func TestRunStillFailingCheckIsReported(t *testing.T) {
	eachShell(t, func(t *testing.T, shell string, s scripts) {
		m := realModule(shell, &bytes.Buffer{}, config.Command{Name: "noop", Check: s.fail, Run: s.pass})

		_, err := apply(t, m)

		if err == nil || !strings.Contains(err.Error(), "still fails") {
			t.Errorf("err = %v", err)
		}
	})
}
