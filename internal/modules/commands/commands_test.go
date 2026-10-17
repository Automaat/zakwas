package commands

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/testutil"
)

var (
	pass = runner.Result{}
	fail = runner.Result{ExitCode: 1}
)

func cmd() config.Command {
	return config.Command{Name: "gke-auth", Check: "command -v gke-gcloud-auth-plugin", Run: "gcloud components install gke-gcloud-auth-plugin"}
}

func run(t *testing.T, fake *runnertest.Fake) ([]engine.Change, error) {
	t.Helper()
	m := &Module{Runner: fake, Commands: []config.Command{cmd()}, Home: "/home/me", GOOS: platform.Linux}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	for _, c := range fake.Calls {
		if c.Dir != m.Home {
			t.Errorf("%s ran in %q, want $HOME", c, c.Dir)
		}
	}
	return changes, err
}

func TestSatisfiedIsSkipped(t *testing.T) {
	fake := runnertest.New().On("sh -c "+cmd().Check, pass)
	changes, err := run(t, fake)
	if err != nil || len(changes) != 0 {
		t.Errorf("changes = %v, err = %v", changes, err)
	}
}

func TestRunsUntilCheckPasses(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, pass).
		On("sh -c "+cmd().Run, pass)

	changes, err := run(t, fake)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, err = %v", changes, err)
	}
	if !fake.Ran("sh -c " + cmd().Run) {
		t.Error("run was not executed")
	}
}

func TestSatisfiedSincePlanning(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, pass)

	if _, err := run(t, fake); err != nil {
		t.Fatal(err)
	}
	if fake.Ran("sh -c " + cmd().Run) {
		t.Error("run must be skipped when an earlier module already satisfied the check")
	}
}

func TestStillFailingAfterRun(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Run, pass)

	_, err := run(t, fake)
	if err == nil || !strings.Contains(err.Error(), "still fails") {
		t.Errorf("err = %v", err)
	}
}

func TestRunFailure(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Run, runner.Result{ExitCode: 2, Stderr: "network down"})

	_, err := run(t, fake)
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Errorf("err = %v", err)
	}
}

func TestHungCheckTimesOut(t *testing.T) {
	testutil.SkipOnWindows(t, "the test hangs a check with sh and sleep; TestHungCheckIsKilledOnWindows does it with PowerShell")
	defer func(d time.Duration) { CheckTimeout = d }(CheckTimeout)
	CheckTimeout = 100 * time.Millisecond

	m := &Module{
		Runner:   &runner.Exec{Detached: true, Grace: time.Second},
		Home:     t.TempDir(),
		GOOS:     platform.Linux,
		Commands: []config.Command{{Name: "hang", Check: "sleep 10", Run: "true"}},
	}
	start := time.Now()
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hang: check timed out") {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("plan took %s; a hung check must not hang it", d)
	}
}

func TestShellChoice(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		shell   string
		missing []string
		want    string
		args    []string
		cmdLine string
	}{
		{"unix default stays sh", platform.Darwin, "", nil, "sh", []string{"-c", "echo hi"}, ""},
		{"linux default stays sh", platform.Linux, "", []string{"pwsh"}, "sh", []string{"-c", "echo hi"}, ""},
		{"windows prefers pwsh", platform.Windows, "", nil, "pwsh", nil, ""},
		{"windows falls back to Windows PowerShell", platform.Windows, "", []string{"pwsh"}, "powershell", nil, ""},
		{"explicit bash", platform.Windows, "bash", nil, "bash", []string{"-c", "echo hi"}, ""},
		{"explicit sh on windows", platform.Windows, "sh", nil, "sh", []string{"-c", "echo hi"}, ""},
		{"explicit powershell", platform.Windows, "powershell", nil, "powershell", nil, ""},
		{"explicit pwsh on unix", platform.Linux, "pwsh", nil, "pwsh", nil, ""},
		{"explicit cmd", platform.Windows, "cmd", nil, "cmd.exe", []string{"/d", "/s", "/c", "echo hi"}, `cmd.exe /d /s /c "echo hi"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runnertest.New().Missing(tt.missing...)
			m := &Module{Runner: fake, Home: "/h", GOOS: tt.goos}

			got, err := m.cmd(config.Command{Name: "n", Shell: tt.shell}, "echo hi")
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tt.want || got.Dir != "/h" || got.CmdLine != tt.cmdLine {
				t.Errorf("cmd = %q in %q with %q, want %q in /h with %q", got.Name, got.Dir, got.CmdLine, tt.want, tt.cmdLine)
			}
			if tt.args != nil && !slices.Equal(got.Args, tt.args) {
				t.Errorf("args = %q, want %q", got.Args, tt.args)
			}
		})
	}
}

func TestPowerShellInvocation(t *testing.T) {
	m := &Module{Runner: runnertest.New(), Home: "/h", GOOS: platform.Windows}
	script := "Write-Output 'a \"b\"'\n} ; exit 0 ; . {"

	got, err := m.cmd(config.Command{Name: "n"}, script)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", psSettings + ";" + script}
	if !slices.Equal(got.Args, want) {
		t.Fatalf("args = %q, want %q: the script is the command's own text, after one line of settings", got.Args, want)
	}
	if !slices.Contains(got.Env, "POWERSHELL_TELEMETRY_OPTOUT=1") {
		t.Errorf("env = %q; want the telemetry opt-out", got.Env)
	}
	if got.Label != "pwsh -Command Write-Output 'a \"b\"'…" {
		t.Errorf("label = %q", got.Label)
	}
	if got.CmdLine != "" || !got.KeepConsoleCP {
		t.Errorf("CmdLine = %q, KeepConsoleCP = %v", got.CmdLine, got.KeepConsoleCP)
	}
}

func TestPowerShellScriptWithUsingRunsUntouched(t *testing.T) {
	tests := []string{
		"using namespace System.Text\nGet-Date",
		"<# a note #>\nusing namespace System.Text",
		"# note\n\nUSING\tmodule Foo",
		"using namespace X; Get-Date",
	}
	for _, script := range tests {
		if got := psScript(script); got != script {
			t.Errorf("psScript(%q) = %q; a using statement must stay first", script, got)
		}
	}
	if got := psScript("Get-Date"); got != psSettings+";Get-Date" {
		t.Errorf("psScript = %q", got)
	}
}

func TestPowerShellOnUnixSkipsTheExecutionPolicy(t *testing.T) {
	m := &Module{Runner: runnertest.New(), Home: "/h", GOOS: platform.Linux}

	got, err := m.cmd(config.Command{Name: "n", Shell: "pwsh"}, "exit 0")

	if err != nil || slices.Contains(got.Args, "-ExecutionPolicy") {
		t.Errorf("args = %q, err = %v; pwsh off Windows has no execution policy", got.Args, err)
	}
}

func TestMissingShellFailsThePlan(t *testing.T) {
	tests := []struct{ goos, shell, missing, want string }{
		{platform.Linux, "pwsh", "pwsh", "shell pwsh is not installed (pwsh not found on PATH)"},
		{platform.Linux, "bash", "bash", "shell bash is not installed"},
		{platform.Darwin, "cmd", "cmd.exe", "shell cmd is not installed (cmd.exe not found on PATH)"},
		{platform.Windows, "powershell", "powershell", "shell powershell is not installed"},
	}
	for _, tt := range tests {
		t.Run(tt.shell+" on "+tt.goos, func(t *testing.T) {
			fake := runnertest.New().Missing(tt.missing)
			m := &Module{Runner: fake, Home: "/h", GOOS: tt.goos, Commands: []config.Command{{Name: "step", Check: "x", Run: "y", Shell: tt.shell}}}

			_, err := m.Plan(context.Background())

			if err == nil || !strings.Contains(err.Error(), "step: "+tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
			if len(fake.Calls) != 0 {
				t.Errorf("ran %v without the shell", fake.Lines())
			}
		})
	}
}

func TestPowerShellEntryKeepsTheCommandSemantics(t *testing.T) {
	check := config.Command{Name: "tool", Check: "Get-Command tool", Run: "Install-Tool", Shell: "pwsh"}
	label := func(script string) string { return "pwsh -Command " + script }
	fake := runnertest.New().
		On(label(check.Check), fail).
		On(label(check.Check), fail).
		On(label(check.Check), pass).
		On(label(check.Run), pass)
	m := &Module{Runner: fake, Commands: []config.Command{check}, Home: "/h", GOOS: platform.Windows}

	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, err = %v", changes, err)
	}
	if changes[0].Diff != check.Run || !changes[0].Streams {
		t.Errorf("change = %+v; the script is still the diff and the output streams", changes[0])
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if !fake.Ran(label(check.Run)) {
		t.Error("run was not executed")
	}
	for _, c := range fake.Calls {
		if c.Dir != "/h" {
			t.Errorf("%s ran in %q", c, c.Dir)
		}
	}
}

func TestEntriesStopAtTheFirstFailureAcrossShells(t *testing.T) {
	fake := runnertest.New().
		On("sh -c a", fail).
		On("sh -c c", fail).
		On("sh -c b", runner.Result{ExitCode: 2, Stderr: "boom"})
	m := &Module{Runner: fake, Home: "/h", GOOS: platform.Linux, Commands: []config.Command{
		{Name: "one", Check: "a", Run: "b"},
		{Name: "two", Check: "c", Run: "d"},
	}}

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatalf("planning failed: %v", err)
	}
	if err = enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if fake.Ran("sh -c d") {
		t.Error("the entry after the failing one ran")
	}
}

func TestShortenKeepsMessagesToOneLine(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct{ in, want string }{
		{"echo hi", "echo hi"},
		{"echo hi\n", "echo hi"},
		{"one\ntwo", "one…"},
		{"one\r\ntwo", "one…"},
		{long, strings.Repeat("x", labelWidth) + "…"},
	}
	for _, tt := range tests {
		if got := shorten(tt.in); got != tt.want {
			t.Errorf("shorten(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestShNotInstalled(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		shell string
		want  bool
	}{
		{"default on unix keeps the raw error", platform.Linux, "", false},
		{"default on macOS keeps the raw error", platform.Darwin, "", false},
		{"explicit sh is checked", platform.Linux, "sh", true},
		{"explicit sh on windows is checked", platform.Windows, "sh", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Module{Runner: runnertest.New().Missing("sh"), Home: "/h", GOOS: tt.goos}

			_, err := m.cmd(config.Command{Name: "n", Shell: tt.shell}, "true")

			if got := err != nil; got != tt.want {
				t.Fatalf("err = %v, want an error: %v", err, tt.want)
			}
			if tt.want && !strings.Contains(err.Error(), "n: shell sh is not installed (sh not found on PATH)") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestLongScriptsFailThePlanWithOneLine(t *testing.T) {
	win := &Module{Runner: runnertest.New(), Home: "/h", GOOS: platform.Windows}
	linux := &Module{Runner: runnertest.New(), Home: "/h", GOOS: platform.Linux}
	mac := &Module{Runner: runnertest.New(), Home: "/h", GOOS: platform.Darwin}
	tests := []struct {
		name   string
		m      *Module
		shell  string
		script string
		want   string
	}{
		{"pwsh over the Windows limit", win, "pwsh", strings.Repeat("a", 33000), "big: the pwsh command line is "},
		{"pwsh within the Windows limit", win, "pwsh", strings.Repeat("a", 30000), ""},
		{"cmd over 8191", win, "cmd", strings.Repeat("a", 8200), "big: the cmd line is "},
		{"cmd at the limit", win, "cmd", strings.Repeat("a", maxCmdLine-len(`cmd.exe /d /s /c ""`)), ""},
		{"bash over the arguments limit", win, "bash", strings.Repeat("a b ", 9000), "big: the bash command line is "},
		{"bash with many backslashes is not counted twice", win, "bash", strings.Repeat(`\`, 17000), ""},
		{"pwsh on Linux at the limit", linux, "pwsh", strings.Repeat("a", maxLinuxArg-len(psSettings+";")), ""},
		{"pwsh on Linux over the limit", linux, "pwsh", strings.Repeat("a", maxLinuxArg-len(psSettings+";")+1), "big: the pwsh script is 131072 bytes, over the 131071 Linux allows in one argument"},
		{"sh on Linux over the limit", linux, "sh", strings.Repeat("a", maxLinuxArg+1), "big: the sh script is 131072 bytes"},
		{"sh on macOS has no such limit", mac, "sh", strings.Repeat("a b ", 90000), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.m.cmd(config.Command{Name: "big", Shell: tt.shell}, tt.script)

			switch {
			case tt.want == "" && err != nil:
				t.Errorf("err = %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "\n")):
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEscapedLenFollowsGoQuoting(t *testing.T) {
	tests := []struct {
		arg  string
		want int
	}{
		{"", 2},
		{"abc", 3},
		{`a\b`, 3},
		{"a b", 5},
		{`a"b`, 4},
		{`a\"b`, 6},
		{`a b\`, 7},
		{"é", 1},
		{"😀", 2},
	}
	for _, tt := range tests {
		if got := escapedLen(tt.arg); got != tt.want {
			t.Errorf("escapedLen(%q) = %d, want %d", tt.arg, got, tt.want)
		}
	}
}

func TestErrorsNameTheScriptOnOneLine(t *testing.T) {
	script := "Write-Output one\nWrite-Output two"
	fake := runnertest.New().On("pwsh -Command Write-Output one…", runner.Result{ExitCode: 4, Stderr: "boom"})
	m := &Module{Runner: fake, Home: "/h", GOOS: platform.Windows, Commands: []config.Command{{Name: "n", Shell: "pwsh", Check: script, Run: "x"}}}

	_, err := m.Plan(context.Background())
	if err != nil {
		t.Fatalf("a failing check only means the step runs: %v", err)
	}
	c, _ := m.cmd(m.Commands[0], script)
	if err := (runner.Result{ExitCode: 4, Stderr: "boom"}).Err(c); err == nil || strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), "ProgressPreference") {
		t.Errorf("err = %v; want the short label only", err)
	}
}
