package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const proposeConfig = `templates:
  files:
    - {src: opencode.json.tmpl, dst: ~/.config/opencode/opencode.json}
agents:
  guard: false
  providers: [opencode]
  permissions:
    deny:
      - rm
`

const opencodeTemplate = `{"permission": {"bash": {{ agentPermissions "opencode" "ask" }}}}` + "\n"

// gitRunner runs git for real, so approve can commit in a temp repo, and
// everything else through the fake.
type gitRunner struct {
	*runnertest.Fake
	exec *runner.Exec
}

func (g gitRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	switch c.Name {
	case "git":
		return g.exec.Run(ctx, c)
	case "brew":
		return runner.Result{}, nil
	}
	return g.Fake.Run(ctx, c)
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func proposeSetup(t *testing.T) (Env, string) {
	t.Helper()
	return proposeSetupConfig(t, proposeConfig)
}

func proposeSetupConfig(t *testing.T, cfg string) (Env, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	env, home := setup(t, cfg)
	root := env.Cwd
	for name, body := range map[string]string{"opencode.json.tmpl": opencodeTemplate, "Brewfile": "brew \"git\"\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "config", "user.email", "t@example.com")
	gitIn(t, root, "config", "user.name", "T")
	gitIn(t, root, "config", "commit.gpgsign", "false")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "init")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	env.Runner = gitRunner{Fake: runnertest.New(), exec: runner.NewExec()}
	env.Now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	return env, home
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func propose(t *testing.T, env Env, args ...string) proposal.Proposal {
	t.Helper()
	r := invoke(env, "", append([]string{"propose", "-json"}, args...)...)
	if r.code != ExitOK {
		t.Fatalf("propose exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	var p proposal.Proposal
	if err := json.Unmarshal([]byte(r.stdout), &p); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	return p
}

func TestProposeSavesWithoutEditing(t *testing.T) {
	env, home := proposeSetup(t)
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))

	p := propose(t, env, "add-permission", "review", "git", "push")
	if after := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")); after != before {
		t.Errorf("propose edited zakwas.yaml:\n%s", after)
	}
	if status := gitIn(t, env.Cwd, "status", "--porcelain"); status != "" {
		t.Errorf("propose dirtied the repo: %s", status)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
		t.Error("propose changed the system")
	}
	if p.Tier != proposal.TierHigh || len(p.Reasons) == 0 || !strings.Contains(p.Reasons[0], "opencode.json") {
		t.Errorf("tier = %s, reasons = %q; a plan touching agent config is high", p.Tier, p.Reasons)
	}
	if !strings.Contains(p.Diff, "+      - git push") || strings.Join(p.Modules, ",") != "templates,agents" {
		t.Errorf("diff = %q, modules = %v", p.Diff, p.Modules)
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(home), p.ID+".json")); err != nil {
		t.Error(err)
	}

	r := invoke(env, "", "proposals")
	if r.code != ExitOK || !strings.Contains(r.stdout, p.ID+"  high  in 7d     review \"git push\"") {
		t.Errorf("proposals:\n%s%s", r.stdout, r.stderr)
	}
	r = invoke(env, "", "proposals", "-json")
	if !strings.Contains(r.stdout, `"id": "`+p.ID+`"`) {
		t.Errorf("proposals -json:\n%s", r.stdout)
	}
}

func TestProposalsEmptyAndOtherConfig(t *testing.T) {
	env, _ := proposeSetup(t)
	if r := invoke(env, "", "proposals"); r.code != ExitOK || !strings.Contains(r.stdout, "No pending proposals") {
		t.Errorf("%s%s", r.stdout, r.stderr)
	}
	if r := invoke(env, "", "proposals", "-json"); !strings.Contains(r.stdout, `"proposals": []`) {
		t.Errorf("%s", r.stdout)
	}
}

func TestProposeRejects(t *testing.T) {
	env, home := proposeSetup(t)
	tests := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"no operation", []string{"propose"}, ExitUsage, "Usage: zakwas propose"},
		{"unknown operation", []string{"propose", "delete-all"}, ExitUsage, "unknown operation"},
		{"missing argument", []string{"propose", "pin-mise", "node"}, ExitUsage, "usage: zakwas propose pin-mise TOOL VERSION"},
		{"stray flag", []string{"propose", "add-plugin", "a@b", "-cask"}, ExitUsage, "add-plugin doesn't take -cask"},
		{"unknown marketplace", []string{"propose", "add-plugin", "a@nowhere"}, ExitErr, "not declared"},
		{"duplicate", []string{"propose", "add-permission", "deny", "rm"}, ExitErr, "already has a rule"},
		{"invalid result", []string{"propose", "add-permission", "deny", "rm -rf *"}, ExitErr, "invalid"},
		{"no brew section", []string{"propose", "add-brew", "jq"}, ExitErr, "no brew section"},
		{"bad decision", []string{"propose", "add-permission", "maybe", "ls"}, ExitUsage, "decision must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := invoke(env, "", tt.args...)
			if r.code != tt.code || !strings.Contains(r.stdout+r.stderr, tt.msg) {
				t.Errorf("exit %d, want %d, %q\nstdout: %s\nstderr: %s", r.code, tt.code, tt.msg, r.stdout, r.stderr)
			}
		})
	}
	if all, _ := (proposal.Store{Dir: proposal.Dir(home)}).List(); len(all) != 0 {
		t.Errorf("rejected proposals were saved: %d", len(all))
	}
}

func TestApproveAppliesAndCommits(t *testing.T) {
	env, home := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	env.StdinTTY = true

	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"(high risk)", "+      - git push", "opencode.json", "Approve " + p.ID + ": apply 1 step(s) and commit zakwas.yaml to main? [y/N]", "Applied 1 of 1 steps", "not pushed"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	yaml := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	if want := proposeConfig + "    review:\n      - git push\n"; yaml != want {
		t.Errorf("zakwas.yaml:\n%s\nwant:\n%s", yaml, want)
	}
	if got := readFile(t, filepath.Join(home, ".config", "opencode", "opencode.json")); !strings.Contains(got, `"git push": "ask"`) {
		t.Errorf("opencode.json:\n%s", got)
	}
	if status := gitIn(t, env.Cwd, "status", "--porcelain"); status != "" {
		t.Errorf("repo is dirty after approve: %s", status)
	}
	if subject := gitIn(t, env.Cwd, "log", "-1", "--format=%s"); subject != `zakwas: review "git push"` {
		t.Errorf("commit subject = %q", subject)
	}
	if body := gitIn(t, env.Cwd, "log", "-1", "--format=%b"); !strings.Contains(body, "Proposal "+p.ID) {
		t.Errorf("commit body = %q", body)
	}
	if n := gitIn(t, env.Cwd, "rev-list", "--count", "HEAD"); n != "2" {
		t.Errorf("commits = %s, want 2", n)
	}
	if r := invoke(env, "", "proposals"); !strings.Contains(r.stdout, "No pending proposals") {
		t.Errorf("approved proposal is still listed:\n%s", r.stdout)
	}
	if _, err := os.Stat(HistoryPath(home)); err != nil {
		t.Errorf("apply wasn't recorded in the history: %v", err)
	}
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitErr || !strings.Contains(r.stderr, "no proposal") {
		t.Errorf("second approve: exit %d\n%s", r.code, r.stderr)
	}
}

func TestApproveAcceptsAnIDPrefix(t *testing.T) {
	env, _ := proposeSetup(t)
	p := propose(t, env, "add-permission", "deny", "dd")
	env.StdinTTY = true
	if r := invoke(env, "n\n", "approve", p.ID[:4]); r.code != ExitErr || !strings.Contains(r.stdout, "aborted") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestApproveDeclined(t *testing.T) {
	env, home := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	env.StdinTTY = true
	for _, answer := range []string{"n\n", "\n", ""} {
		r := invoke(env, answer, "approve", p.ID)
		if r.code != ExitErr || !strings.Contains(r.stdout, "aborted") {
			t.Errorf("answer %q: exit %d\n%s%s", answer, r.code, r.stdout, r.stderr)
		}
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("a declined approve changed the repo")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
		t.Error("a declined approve changed the system")
	}
	if r := invoke(env, "", "proposals"); !strings.Contains(r.stdout, p.ID) {
		t.Error("a declined proposal is gone")
	}
}

func TestApproveRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, env *Env)
		msg    string
	}{
		{"base changed", func(t *testing.T, env *Env) {
			path := filepath.Join(env.Cwd, "zakwas.yaml")
			if err := os.WriteFile(path, []byte(readFile(t, path)+"# edited\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitIn(t, env.Cwd, "commit", "-q", "-am", "edit")
		}, "zakwas.yaml changed since proposal"},
		{"older than 7 days", func(t *testing.T, env *Env) {
			env.Now = func() time.Time { return time.Date(2026, 10, 8, 12, 0, 1, 0, time.UTC) }
		}, "expired on 2026-10-08"},
		{"uncommitted changes", func(t *testing.T, env *Env) {
			gitIn(t, env.Cwd, "rm", "-q", "--cached", "zakwas.yaml")
		}, "zakwas.yaml has uncommitted changes"},
		{"detached head", func(t *testing.T, env *Env) {
			gitIn(t, env.Cwd, "checkout", "-q", "--detach")
		}, "detached HEAD"},
		{"not a terminal", func(t *testing.T, env *Env) { env.StdinTTY = false }, "stdin is not a terminal"},
		{"agent session", func(t *testing.T, env *Env) {
			env.AgentSession = "Codex"
			if err := os.WriteFile(filepath.Join(env.Cwd, "zakwas.yaml"), []byte(strings.Replace(proposeConfig, "guard: false", "guard: true", 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			gitIn(t, env.Cwd, "commit", "-q", "-am", "guard")
		}, "refusing to approve from a Codex session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, home := proposeSetup(t)
			p := propose(t, env, "add-permission", "review", "git", "push")
			env.StdinTTY = true
			tt.mutate(t, &env)
			head := gitIn(t, env.Cwd, "rev-parse", "HEAD")
			r := invoke(env, "y\n", "approve", p.ID)
			if r.code == ExitOK || !strings.Contains(r.stderr, tt.msg) {
				t.Errorf("exit %d, want a refusal mentioning %q\nstdout: %s\nstderr: %s", r.code, tt.msg, r.stdout, r.stderr)
			}
			if gitIn(t, env.Cwd, "rev-parse", "HEAD") != head {
				t.Error("a refused approve committed")
			}
			if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
				t.Error("a refused approve changed the system")
			}
			if _, err := os.Stat(filepath.Join(proposal.Dir(home), p.ID+".json")); err != nil {
				t.Errorf("a refused approve deleted the proposal: %v", err)
			}
		})
	}
}

func TestApproveAllowsAgentsWhenTheGuardIsOff(t *testing.T) {
	env, _ := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	env.StdinTTY, env.AgentSession = true, "Codex"
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestApproveRestoresTheFilesWhenApplyFails(t *testing.T) {
	env, home := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	env.StdinTTY = true
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only directory")
	}
	config := filepath.Join(home, ".config")
	if err := os.Mkdir(config, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config, 0o700) })
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	head := gitIn(t, env.Cwd, "rev-parse", "HEAD")

	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "restored the config files") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" || gitIn(t, env.Cwd, "rev-parse", "HEAD") != head {
		t.Error("a failed approve left the repo changed")
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(home), p.ID+".json")); err != nil {
		t.Errorf("the proposal should stay pending: %v", err)
	}
}

func TestApproveLeavesOtherChangesUncommitted(t *testing.T) {
	env, _ := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	env.StdinTTY = true
	other := filepath.Join(env.Cwd, "notes.txt")
	if err := os.WriteFile(other, []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, env.Cwd, "add", "notes.txt")
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if files := gitIn(t, env.Cwd, "show", "--name-only", "--format=", "HEAD"); files != "zakwas.yaml" {
		t.Errorf("commit holds %q, want only zakwas.yaml", files)
	}
	if status := gitIn(t, env.Cwd, "status", "--porcelain"); status != "A  notes.txt" {
		t.Errorf("status = %q, the user's staged file should stay staged", status)
	}
}

type editWhileAsking struct {
	path string
	done bool
}

func (r *editWhileAsking) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		data, err := os.ReadFile(r.path)
		if err != nil {
			return 0, err
		}
		if err := os.WriteFile(r.path, append(data, "# saved while the prompt waited\n"...), 0o644); err != nil {
			return 0, err
		}
	}
	return copy(p, "y\n"), nil
}

func TestApproveKeepsAnEditSavedWhileItAsks(t *testing.T) {
	env, _ := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	env.StdinTTY = true
	path := filepath.Join(env.Cwd, "zakwas.yaml")
	var stdout, stderr strings.Builder
	env.Args = []string{"approve", p.ID}
	env.Stdin, env.Stdout, env.Stderr = &editWhileAsking{path: path}, &stdout, &stderr
	if code := Main(context.Background(), env); code != ExitErr || !strings.Contains(stderr.String(), "uncommitted changes") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	if got := readFile(t, path); !strings.Contains(got, "saved while the prompt waited") || strings.Contains(got, "git push") {
		t.Errorf("approve overwrote the user's edit:\n%s", got)
	}
}

func TestForgedProposalTextIsNotTrusted(t *testing.T) {
	env, home := proposeSetup(t)
	p := propose(t, env, "add-permission", "review", "git", "push")
	file := filepath.Join(proposal.Dir(home), p.ID+".json")
	forged := strings.Replace(readFile(t, file), `"summary": "review \"git push\""`, `"summary": "tidy up\u001b[8m"`, 1)
	forged = strings.Replace(forged, `"tier": "high"`, `"tier": "low"`, 1)
	if err := os.WriteFile(file, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := invoke(env, "", "proposals"); strings.Contains(r.stdout, "\x1b") {
		t.Errorf("proposals prints control characters: %q", r.stdout)
	}
	env.StdinTTY = true
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitOK || strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, `review "git push" (high risk)`) {
		t.Fatalf("exit %d\nstdout: %q\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if subject := gitIn(t, env.Cwd, "log", "-1", "--format=%s"); subject != `zakwas: review "git push"` {
		t.Errorf("commit subject = %q", subject)
	}
	if body := gitIn(t, env.Cwd, "log", "-1", "--format=%b"); !strings.Contains(body, "(high risk)") {
		t.Errorf("commit body = %q", body)
	}
}

func TestApproveChecksTheConfigFileEvenWhenItIsNotEdited(t *testing.T) {
	env, _ := proposeSetupConfig(t, proposeConfig+"brew: {file: Brewfile}\n")
	p := propose(t, env, "add-brew", "jq")
	env.StdinTTY = true
	path := filepath.Join(env.Cwd, "zakwas.yaml")
	if err := os.WriteFile(path, []byte(readFile(t, path)+"# wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "zakwas.yaml, Brewfile has uncommitted changes") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(readFile(t, filepath.Join(env.Cwd, "Brewfile")), "jq") {
		t.Error("the refused approve edited the Brewfile")
	}
}

func TestApproveRefusesAnIgnoredFile(t *testing.T) {
	env, _ := proposeSetupConfig(t, proposeConfig+"brew: {file: Brewfile}\n")
	p := propose(t, env, "add-brew", "jq")
	env.StdinTTY = true
	if err := os.WriteFile(filepath.Join(env.Cwd, ".gitignore"), []byte("Brewfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, env.Cwd, "add", ".gitignore")
	gitIn(t, env.Cwd, "rm", "-q", "--cached", "Brewfile")
	gitIn(t, env.Cwd, "commit", "-q", "-m", "ignore")
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "Brewfile is ignored by git") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestApproveBrewCommitsOnlyTheBrewfile(t *testing.T) {
	env, _ := proposeSetupConfig(t, proposeConfig+"brew: {file: Brewfile}\n")
	p := propose(t, env, "add-brew", "jq")
	env.StdinTTY = true
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if files := gitIn(t, env.Cwd, "show", "--name-only", "--format=", "HEAD"); files != "Brewfile" {
		t.Errorf("commit holds %q", files)
	}
}
