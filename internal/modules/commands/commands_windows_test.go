//go:build windows

package commands

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

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

func TestCmdQuotingRules(t *testing.T) {
	dir := t.TempDir()
	spaced := filepath.Join(dir, "sp ace")
	if err := os.Mkdir(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaced, "tool.cmd"), []byte("@echo off\r\necho got %1\r\nexit /b 3\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZK_DIR", dir)
	t.Setenv("ZK_VAL", "x y")
	tests := []struct {
		name, line string
		wantOut    []string
		wantExit   int
	}{
		{"a variable is expanded", `echo %ZK_VAL%`, []string{"x y"}, 0},
		{"an unset variable stays as written", `echo %ZK_UNSET_VAR%`, []string{"%ZK_UNSET_VAR%"}, 0},
		{"a caret escapes an ampersand", `echo a^&b`, []string{"a&b"}, 0},
		{"a bare ampersand chains commands", `echo one& echo two`, []string{"one", "two"}, 0},
		{"quotes stay in what echo prints", `echo "a b"`, []string{`"a b"`}, 0},
		{"a program path with spaces runs from quotes", `"%ZK_DIR%\sp ace\tool.cmd" arg`, []string{"got arg"}, 3},
		{"exit code", `exit /b 7`, nil, 7},
		{"the first failing command of a chain with && stops it", `exit /b 4 && echo never`, nil, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Module{Runner: &runner.Exec{NoStdin: true}, Home: dir}
			c, err := m.cmd(config.Command{Name: "n", Shell: "cmd"}, tt.line)
			if err != nil {
				t.Fatal(err)
			}

			res, err := m.Runner.Run(context.Background(), c)

			if err != nil || res.ExitCode != tt.wantExit {
				t.Fatalf("exit %d, err %v, want %d\nstdout: %s\nstderr: %s", res.ExitCode, err, tt.wantExit, res.Stdout, res.Stderr)
			}
			var got []string
			for line := range strings.Lines(res.Stdout) {
				got = append(got, strings.TrimRight(line, " \r\n"))
			}
			if !slices.Equal(got, tt.wantOut) {
				t.Errorf("stdout = %q, want %q", got, tt.wantOut)
			}
		})
	}
}

func TestEscapedLenMatchesTheStandardLibrary(t *testing.T) {
	args := []string{
		"", "a", "a b", `a"b`, `a\b`, `a\"b`, `a\\"b`, `a b\`, `a b\\`, `"`, `""`, `\`, `\\`, "tab\there",
		`C:\Program Files\x y\`, `say "hi there" \`, "é ü", "😀 x", strings.Repeat(`\`, 50) + `"`, strings.Repeat(`a\ `, 20),
	}
	for _, a := range args {
		if got, want := escapedLen(a), len(utf16.Encode([]rune(syscall.EscapeArg(a)))); got != want {
			t.Errorf("escapedLen(%q) = %d, syscall.EscapeArg gives %d", a, got, want)
		}
	}
}
