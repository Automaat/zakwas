package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/testutil"
)

const workLayerConfig = "agents:\n  permissions:\n    deny:\n      - dd\n"

func layerProposeSetup(t *testing.T) (env Env, home, work string) {
	t.Helper()
	env, home = proposeSetup(t)
	work = writeFiles(t, map[string]string{"zakwas.yaml": workLayerConfig})
	gitIn(t, work, "init", "-q", "-b", "main")
	gitIn(t, work, "config", "user.email", "t@example.com")
	gitIn(t, work, "config", "user.name", "T")
	gitIn(t, work, "config", "commit.gpgsign", "false")
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "-q", "-m", "init")
	mustOK(t, env, "layer", "add", "work", work)
	mustOK(t, env, "apply", "-y")
	return env, home, work
}

func TestProposeLayerEditsAndCommitsInTheLayerOnly(t *testing.T) {
	env, home, work := layerProposeSetup(t)
	mainBefore := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	mainHead := gitIn(t, env.Cwd, "rev-parse", "HEAD")

	p := propose(t, env, "--layer", "work", "add-permission", "review", "git", "push")

	if p.Layer != "work" || !strings.Contains(p.Diff, "+++ b/zakwas.yaml") || !strings.Contains(p.Diff, "+      - git push") {
		t.Errorf("layer = %q, diff:\n%s", p.Layer, p.Diff)
	}
	if got := readFile(t, filepath.Join(work, "zakwas.yaml")); got != workLayerConfig {
		t.Errorf("propose edited the layer's file:\n%s", got)
	}
	if r := invoke(env, "", "proposals"); !strings.Contains(r.stdout, "(layer work)") {
		t.Errorf("proposals does not name the layer:\n%s", r.stdout)
	}

	env.StdinTTY = true
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitOK {
		t.Fatalf("approve: exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"for layer work", "commit zakwas.yaml to main of layer work?", "of layer work; not pushed"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("approve output lacks %q:\n%s", want, r.stdout)
		}
	}
	if got, want := readFile(t, filepath.Join(work, "zakwas.yaml")), workLayerConfig+"    review:\n      - git push\n"; got != want {
		t.Errorf("layer zakwas.yaml:\n%s\nwant:\n%s", got, want)
	}
	if n := gitIn(t, work, "rev-list", "--count", "HEAD"); n != "2" {
		t.Errorf("layer commits = %s, want 2", n)
	}
	if body := gitIn(t, work, "log", "-1", "--format=%b"); !strings.Contains(body, "for layer work") {
		t.Errorf("commit body = %q", body)
	}
	if gitIn(t, work, "status", "--porcelain") != "" {
		t.Error("the layer repo is dirty after approve")
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != mainBefore || gitIn(t, env.Cwd, "rev-parse", "HEAD") != mainHead || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("approve touched the main config's repo")
	}
	if got := readFile(t, filepath.Join(home, ".config", "opencode", "opencode.json")); !strings.Contains(got, `"git push": "ask"`) {
		t.Errorf("opencode.json lacks the layer's rule:\n%s", got)
	}
	mustOK(t, env, "check")
	if r := invoke(env, "", "plan"); !strings.Contains(r.stdout, "No changes") {
		t.Errorf("second plan is not empty:\n%s", r.stdout)
	}
}

func TestProposeRemovalFollowsTheEntryIntoItsLayer(t *testing.T) {
	env, _, work := layerProposeSetup(t)

	p := propose(t, env, "remove-permission", "deny", "dd")

	if p.Layer != "work" {
		t.Fatalf("layer = %q, want the layer that holds the rule", p.Layer)
	}
	env.StdinTTY = true
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("approve: exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if got := readFile(t, filepath.Join(work, "zakwas.yaml")); strings.Contains(got, "dd") {
		t.Errorf("the rule is still in the layer:\n%s", got)
	}
	if gitIn(t, env.Cwd, "status", "--porcelain") != "" || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
		t.Error("approve touched the main config's repo")
	}
	mustOK(t, env, "check")

	r := invoke(env, "", "propose", "--layer", "work", "remove-permission", "deny", "rm")
	if r.code != ExitErr || !strings.Contains(r.stderr, `not in layer "work"`) {
		t.Errorf("a main rule named as the layer's: exit %d\n%s", r.code, r.stderr)
	}
}

func TestProposeUnregisteredLayerFailsInOneLine(t *testing.T) {
	env, _, _ := layerProposeSetup(t)

	r := invoke(env, "", "propose", "--layer", "nope", "add-permission", "review", "git", "push")

	if r.code != ExitErr || !strings.Contains(r.stderr, `layer "nope" is not registered`) || strings.Count(strings.TrimSpace(r.stderr), "\n") != 0 {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
	if all, _ := (proposal.Store{Dir: proposal.Dir(env.Home)}).List(); len(all) != 0 {
		t.Errorf("a refused proposal was saved: %d", len(all))
	}
}

func TestApproveRefusesAProposalForALayerThatWasRemoved(t *testing.T) {
	env, _, work := layerProposeSetup(t)
	p := propose(t, env, "--layer", "work", "add-permission", "review", "git", "push")
	mustOK(t, env, "layer", "remove", "work")
	env.StdinTTY = true

	r := invoke(env, "y\n", "approve", p.ID)

	if r.code != ExitErr || !strings.Contains(r.stderr, `edits layer "work", which is not registered`) {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
	if gitIn(t, work, "rev-list", "--count", "HEAD") != "1" || readFile(t, filepath.Join(work, "zakwas.yaml")) != workLayerConfig {
		t.Error("the layer repo changed")
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(env.Home), p.ID+".json")); err != nil {
		t.Errorf("the proposal should stay pending: %v", err)
	}
}

func TestApproveChecksTheLayerRepoNotTheMainOne(t *testing.T) {
	env, _, work := layerProposeSetup(t)
	p := propose(t, env, "--layer", "work", "add-permission", "review", "git", "push")
	env.StdinTTY = true
	if err := os.WriteFile(filepath.Join(env.Cwd, "Brewfile"), []byte("brew \"git\"\nbrew \"jq\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "zakwas.yaml"), []byte(workLayerConfig+"# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "changed since proposal") {
		t.Fatalf("exit %d\nstderr: %s", r.code, r.stderr)
	}

	gitIn(t, work, "checkout", "--", "zakwas.yaml")
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("approve with a dirty main repo: exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if status := gitIn(t, env.Cwd, "status", "--porcelain"); status != "M Brewfile" {
		t.Errorf("main repo status = %q, want its own change left alone", status)
	}
}

func TestApproveRestoresTheLayerFilesWhenApplyFails(t *testing.T) {
	testutil.SkipOnWindows(t, "directory modes have no Windows equivalent")
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only directory")
	}
	env, home, work := layerProposeSetup(t)
	p := propose(t, env, "--layer", "work", "add-permission", "review", "git", "push")
	env.StdinTTY = true
	dir := filepath.Join(home, ".config", "opencode")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	r := invoke(env, "y\n", "approve", p.ID)

	if r.code != ExitErr || !strings.Contains(r.stderr, "restored the config files") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if readFile(t, filepath.Join(work, "zakwas.yaml")) != workLayerConfig || gitIn(t, work, "status", "--porcelain") != "" || gitIn(t, work, "rev-list", "--count", "HEAD") != "1" {
		t.Error("a failed approve left the layer repo changed")
	}
}

func TestProposeBatchTakesTheLayerAsADefault(t *testing.T) {
	env, _, _ := layerProposeSetup(t)
	ops := `[{"kind":"add-permission","decision":"review","command":"git push"},{"kind":"add-permission","decision":"review","command":"git pull","layer":"work"}]`
	file := filepath.Join(t.TempDir(), "ops.json")
	if err := os.WriteFile(file, []byte(ops), 0o644); err != nil {
		t.Fatal(err)
	}

	p := propose(t, env, "--layer", "work", "batch", file)
	if p.Layer != "work" || len(p.Ops) != 2 || p.Ops[0].Layer != "work" {
		t.Errorf("layer = %q, ops = %+v", p.Layer, p.Ops)
	}

	r := invoke(env, "", "propose", "--layer", "lab", "batch", file)
	if r.code != ExitUsage || !strings.Contains(r.stderr, `names layer "work", but the proposal's layer is "lab"`) {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}

	r = invoke(env, "", "propose", "batch", file)
	if r.code != ExitErr || !strings.Contains(r.stderr, "committed in one repo") {
		t.Errorf("a batch across two repos: exit %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestProposeRejectsABadLayerName(t *testing.T) {
	env, _, _ := layerProposeSetup(t)

	r := invoke(env, "", "propose", "--layer", "Bad Name", "add-permission", "review", "git", "push")

	if r.code != ExitUsage || !strings.Contains(r.stderr, "layer name may only hold") {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestMCPProposeTargetsALayer(t *testing.T) {
	env, _, work := layerProposeSetup(t)
	c := startMCP(t, env, formClient)

	single := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-permission", "decision": "review", "command": "git push", "layer": "work"}, nil).text)
	if single["layer"] != "work" {
		t.Errorf("single operation: %v", single)
	}
	batch := parseJSON(t, c.tool("zakwas_propose", map[string]any{"layer": "work", "ops": []map[string]any{{"kind": "add-permission", "decision": "review", "command": "git pull"}}}, nil).text)
	if batch["layer"] != "work" {
		t.Errorf("ops with a default layer: %v", batch)
	}
	if listed := c.tool("zakwas_proposals", map[string]any{}, nil).text; !strings.Contains(listed, `"layer": "work"`) {
		t.Errorf("zakwas_proposals does not name the layer:\n%s", listed)
	}
	if readFile(t, filepath.Join(work, "zakwas.yaml")) != workLayerConfig {
		t.Error("the tool edited the layer's file")
	}
	mixed := map[string]any{"layer": "work", "kind": "add-brew", "name": "jq", "ops": []map[string]any{{"kind": "add-brew", "name": "jq"}}}
	if r := c.tool("zakwas_propose", mixed, nil); !r.isErr {
		t.Error("ops with single-operation fields should be refused")
	}
	unknown := map[string]any{"kind": "add-permission", "decision": "review", "command": "git fetch", "layer": "nope"}
	if r := c.tool("zakwas_propose", unknown, nil); !r.isErr || !strings.Contains(r.text, `layer "nope" is not registered`) {
		t.Errorf("unregistered layer: %v %s", r.isErr, r.text)
	}
}

func TestMCPApplyRefusesAProposalForARemovedLayer(t *testing.T) {
	env, _, work := layerProposeSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	p := propose(t, env, "--layer", "work", "add-permission", "deny", "mkfs")
	mustOK(t, env, "layer", "remove", "work")

	r := c.tool("zakwas_apply", map[string]any{"id": p.ID}, accept)

	if !r.isErr || !strings.Contains(r.text, `edits layer "work", which is not registered`) {
		t.Errorf("isErr = %v\n%s", r.isErr, r.text)
	}
	if gitIn(t, work, "rev-list", "--count", "HEAD") != "1" {
		t.Error("the layer repo changed")
	}
}

type layerBrewRunner struct{ brewRunner }

func (b layerBrewRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name == "brew" && len(c.Args) > 2 && c.Args[0] == "bundle" && c.Args[1] == "check" && strings.Contains(c.Stdin, "stern") {
		return runner.Result{ExitCode: 1, Stdout: "→ Formula stern needs to be installed.\n"}, nil
	}
	return b.brewRunner.Run(ctx, c)
}

func brewLayerSetup(t *testing.T) (Env, string) {
	t.Helper()
	env, _ := brewSetup(t)
	env.Runner = layerBrewRunner{env.Runner.(brewRunner)}
	work := writeFiles(t, map[string]string{"zakwas.yaml": "brew: {file: Brewfile}\n", "Brewfile": "brew \"kubectl\"\n"})
	gitIn(t, work, "init", "-q", "-b", "main")
	gitIn(t, work, "config", "user.email", "t@example.com")
	gitIn(t, work, "config", "user.name", "T")
	gitIn(t, work, "config", "commit.gpgsign", "false")
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "-q", "-m", "init")
	mustOK(t, env, "layer", "add", "work", work)
	return env, work
}

func TestMCPApplyCommitsInTheLayerRepo(t *testing.T) {
	env, work := brewLayerSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	p := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "stern", "layer": "work"}, nil).text)
	if p["tier"] != "low" || p["layer"] != "work" {
		t.Fatalf("proposal = %v", p)
	}

	r := c.tool("zakwas_apply", map[string]any{"id": p["id"]}, accept)

	if r.isErr || len(r.elicited) != 1 || !strings.Contains(r.elicited[0], "commit Brewfile to main of layer work?") {
		t.Fatalf("isErr = %v, asked = %q\n%s", r.isErr, r.elicited, r.text)
	}
	if got := readFile(t, filepath.Join(work, "Brewfile")); got != "brew \"kubectl\"\nbrew \"stern\"\n" {
		t.Errorf("layer Brewfile:\n%s", got)
	}
	if files := gitIn(t, work, "show", "--name-only", "--format=", "HEAD"); files != "Brewfile" || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
		t.Errorf("layer commit holds %q, main commits %s", files, gitIn(t, env.Cwd, "rev-list", "--count", "HEAD"))
	}
}

func TestProposeBrewToALayerEditsItsBrewfile(t *testing.T) {
	env, work := brewLayerSetup(t)

	p := propose(t, env, "add-brew", "--layer", "work", "stern")

	if p.Layer != "work" || p.Tier != proposal.TierLow || !strings.Contains(p.Diff, "+brew \"stern\"") || strings.Join(p.Modules, ",") != "brew" {
		t.Errorf("layer = %q, tier = %s, modules = %v, diff:\n%s", p.Layer, p.Tier, p.Modules, p.Diff)
	}
	planned, _ := json.Marshal(p.Plan)
	if !strings.Contains(string(planned), "stern") {
		t.Errorf("the plan does not install the layer's package:\n%s", planned)
	}
	if readFile(t, filepath.Join(work, "Brewfile")) != "brew \"kubectl\"\n" {
		t.Error("propose edited the layer's Brewfile")
	}

	r := invoke(env, "", "propose", "add-brew", "kubectl")
	if r.code != ExitErr || !strings.Contains(r.stderr, "already in the Brewfile") {
		t.Errorf("a package a layer's Brewfile has: exit %d\n%s", r.code, r.stderr)
	}
}
