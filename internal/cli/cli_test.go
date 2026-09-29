package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

type result struct {
	code           int
	stdout, stderr string
}

func setup(t *testing.T, cfg string) (Env, string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	files := map[string]string{
		"zakwas.yaml":    cfg,
		"dotfiles/zshrc": "export EDITOR=vim\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Env{Home: home, Cwd: root, Runner: runnertest.New()}, home
}

func invoke(env Env, stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	env.Args, env.Stdin, env.Stdout, env.Stderr = args, strings.NewReader(stdin), &stdout, &stderr
	code := Main(context.Background(), env)
	return result{code, stdout.String(), stderr.String()}
}

const linksOnly = "links: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n"

func TestExitCodes(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		args []string
		want int
		msg  string
	}{
		{"no command", linksOnly, nil, ExitUsage, "Usage"},
		{"unknown command", linksOnly, []string{"destroy"}, ExitUsage, `unknown command "destroy"`},
		{"extra args", linksOnly, []string{"plan", "extra"}, ExitUsage, "Usage"},
		{"unknown module", linksOnly, []string{"--only", "links,nope", "plan"}, ExitUsage, `module "nope"`},
		{"unconfigured module", linksOnly, []string{"--only", "brew", "plan"}, ExitUsage, `module "brew"`},
		{"invalid config", "links: [{src: x}]", []string{"plan"}, ExitErr, "src and dst are required"},
		{"plan with drift", linksOnly, []string{"plan"}, ExitOK, "+ ~/.zshrc"},
		{"check with drift", linksOnly, []string{"check"}, ExitDrift, "+ ~/.zshrc"},
		{"flags after command", linksOnly, []string{"check", "--only", "links"}, ExitDrift, "Plan: 1 to add"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, tt.cfg)
			r := invoke(env, "", tt.args...)
			if r.code != tt.want {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, tt.want, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout+r.stderr, tt.msg) {
				t.Errorf("output does not mention %q\nstdout: %s\nstderr: %s", tt.msg, r.stdout, r.stderr)
			}
		})
	}
}

func TestApplyConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		tty      bool
		stdin    string
		args     []string
		want     int
		applied  bool
		contains string
	}{
		{"answer no", true, "n\n", []string{"apply"}, ExitErr, false, "aborted"},
		{"no answer", true, "", []string{"apply"}, ExitErr, false, "aborted"},
		{"answer yes", true, "yes\n", []string{"apply"}, ExitOK, true, "Apply 1 step(s)? [y/N]"},
		{"no terminal", false, "yes\n", []string{"apply"}, ExitUsage, false, "stdin is not a terminal"},
		{"no terminal with -y", false, "", []string{"apply", "-y"}, ExitOK, true, "Applied 1 of 1 steps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, home := setup(t, linksOnly)
			env.StdinTTY = tt.tty
			r := invoke(env, tt.stdin, tt.args...)
			if r.code != tt.want || !strings.Contains(r.stdout+r.stderr, tt.contains) {
				t.Fatalf("exit %d, stdout: %s stderr: %s", r.code, r.stdout, r.stderr)
			}
			_, err := os.Lstat(filepath.Join(home, ".zshrc"))
			if applied := err == nil; applied != tt.applied {
				t.Errorf("applied = %v, want %v", applied, tt.applied)
			}
		})
	}
}

func TestApplyNothingToDo(t *testing.T) {
	env, _ := setup(t, linksOnly)
	if r := invoke(env, "", "apply", "-y"); r.code != ExitOK {
		t.Fatal(r)
	}
	r := invoke(env, "", "apply")
	if r.code != ExitOK || !strings.Contains(r.stdout, "No changes") {
		t.Errorf("second apply: %+v", r)
	}
	if r := invoke(env, "", "check"); r.code != ExitOK {
		t.Errorf("check after apply: %+v", r)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env, _ := setup(t, linksOnly)
	cfg := filepath.Join(env.Cwd, "zakwas.yaml")
	env.Cwd = t.TempDir()
	if r := invoke(env, "", "plan"); r.code != ExitErr {
		t.Fatalf("without config: %+v", r)
	}
	t.Setenv("ZAKWAS_CONFIG", cfg)
	if r := invoke(env, "", "plan"); r.code != ExitOK {
		t.Errorf("with ZAKWAS_CONFIG: %+v", r)
	}
	if r := invoke(env, "", "-c", cfg, "plan"); r.code != ExitOK {
		t.Errorf("with -c: %+v", r)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestOutputFailureFailsTheRun(t *testing.T) {
	env, _ := setup(t, linksOnly)
	env.Args, env.Stdin, env.Stdout, env.Stderr = []string{"plan"}, strings.NewReader(""), brokenWriter{}, &bytes.Buffer{}
	if code := Main(context.Background(), env); code != ExitErr {
		t.Errorf("exit %d, want %d when stdout is broken", code, ExitErr)
	}
}

const linksAndBrew = linksOnly + "brew: {file: Brewfile}\n"

func TestPlanFailureDoesNotBlockOtherModules(t *testing.T) {
	env, home := setup(t, linksAndBrew)
	if err := os.WriteFile(filepath.Join(env.Cwd, "Brewfile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "apply", "-y")
	if r.code != ExitErr {
		t.Errorf("exit %d, want %d", r.code, ExitErr)
	}
	if !strings.Contains(r.stdout, "brew: plan failed") || !strings.Contains(r.stderr, "plan brew") {
		t.Errorf("stdout: %s\nstderr: %s", r.stdout, r.stderr)
	}
	if _, err := os.Lstat(filepath.Join(home, ".zshrc")); err != nil {
		t.Errorf("links must still apply when brew fails to plan: %v", err)
	}
	if r := invoke(env, "", "check"); r.code != ExitErr {
		t.Errorf("check with a planning failure: exit %d, want %d", r.code, ExitErr)
	}
	if r := invoke(env, "", "plan"); r.code != ExitErr {
		t.Errorf("plan with a planning failure: exit %d, want %d", r.code, ExitErr)
	}
}

func TestCorruptStateFailsOnlyFileModules(t *testing.T) {
	env, home := setup(t, linksOnly+"files: [{src: dotfiles/zshrc, dst: ~/.zshenv}]\n")
	statePath := filepath.Join(home, ".local/state/zakwas/files.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "apply", "-y")

	if r.code != ExitErr {
		t.Errorf("exit %d, want %d", r.code, ExitErr)
	}
	if !strings.Contains(r.stdout, "files: plan failed") || !strings.Contains(r.stderr, "files.json") {
		t.Errorf("stdout: %s\nstderr: %s", r.stdout, r.stderr)
	}
	if _, err := os.Lstat(filepath.Join(home, ".zshrc")); err != nil {
		t.Errorf("links must still apply with a corrupt state file: %v", err)
	}
}

func TestApplyRecordsHistory(t *testing.T) {
	env, home := setup(t, linksOnly)
	env.Now = func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	if r := invoke(env, "", "apply", "-y"); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	data, err := os.ReadFile(HistoryPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var entry historyEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("%v in %q", err, data)
	}
	if !entry.Time.Equal(env.Now()) || len(entry.Changes) != 1 || !strings.HasPrefix(entry.Changes[0], "links: + ~/.zshrc") {
		t.Errorf("entry = %+v", entry)
	}
	if r := invoke(env, "", "apply", "-y"); r.code != ExitOK || !strings.Contains(r.stdout, "No changes") {
		t.Fatalf("%+v", r)
	}
	if data2, _ := os.ReadFile(HistoryPath(home)); !bytes.Equal(data, data2) {
		t.Error("a no-op apply must not add history")
	}
}

// cancelAware fails once ctx is done, as exec does.
type cancelAware struct{ runner.Runner }

func (r cancelAware) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if err := ctx.Err(); err != nil {
		return runner.Result{}, err
	}
	return r.Runner.Run(ctx, c)
}

func TestInterruptedApplyRecordsCommit(t *testing.T) {
	home := t.TempDir()
	fake := runnertest.New().
		OnOK("git -C /repo rev-parse HEAD", "abc123\n").
		OnOK("git -C /repo status --porcelain", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := Env{Home: home, Runner: cancelAware{fake}}
	if err := recordHistory(ctx, env, "/repo", nil, ctx.Err()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(HistoryPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var entry historyEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Commit != "abc123" || entry.Error == "" {
		t.Errorf("entry = %+v", entry)
	}
}

func TestRepoWarnings(t *testing.T) {
	root := "/repo"
	fake := runnertest.New().
		OnOK("git -C /repo status --porcelain", " M zakwas.yaml\n").
		OnOK("git -C /repo rev-parse --abbrev-ref HEAD", "feat/x\n").
		OnOK("git -C /repo rev-list --count HEAD..@{upstream}", "3\n")
	got := strings.Join(repoWarnings(context.Background(), fake, root), "\n")
	for _, want := range []string{"uncommitted changes", "branch feat/x", "3 commit(s) behind"} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings %q missing %q", got, want)
		}
	}

	clean := runnertest.New().
		OnOK("git -C /repo status --porcelain", "").
		OnOK("git -C /repo rev-parse --abbrev-ref HEAD", "main\n").
		OnOK("git -C /repo rev-list --count HEAD..@{upstream}", "0\n")
	if w := repoWarnings(context.Background(), clean, root); len(w) != 0 {
		t.Errorf("clean main: %v", w)
	}
	if w := repoWarnings(context.Background(), runnertest.New(), root); len(w) != 0 {
		t.Errorf("not a git repo must not warn: %v", w)
	}
}

func TestUpgradeRefreshesBrewFirst(t *testing.T) {
	env, _ := setup(t, linksOnly)
	if r := invoke(env, "", "upgrade", "-y"); r.code != ExitUsage || !strings.Contains(r.stderr, "needs a brew section") {
		t.Errorf("upgrade without brew: %+v", r)
	}

	env, _ = setup(t, linksAndBrew)
	if err := os.WriteFile(filepath.Join(env.Cwd, "Brewfile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := runnertest.New().On("brew update --quiet", runner.Result{ExitCode: 1, Stderr: "offline"})
	env.Runner = fake
	r := invoke(env, "", "upgrade", "-y")
	if r.code != ExitErr || !strings.Contains(r.stderr, "offline") {
		t.Errorf("failed refresh must stop the upgrade: %+v", r)
	}
	if lines := fake.Lines(); len(lines) != 1 || lines[0] != "brew update --quiet" {
		t.Errorf("ran %v; nothing may run after a failed refresh", lines)
	}
}

func TestPlanDiff(t *testing.T) {
	env, _ := setup(t, "files: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n")
	r := invoke(env, "", "plan", "--diff")
	if r.code != ExitOK || !strings.Contains(r.stdout, "+export EDITOR=vim") {
		t.Errorf("%+v", r)
	}
	if r := invoke(env, "", "plan"); strings.Contains(r.stdout, "+export") {
		t.Errorf("diff shown without --diff: %s", r.stdout)
	}
}
