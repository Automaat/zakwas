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
)

const brewBatch = `[{"kind":"add-brew","name":"jq"},{"kind":"add-brew","name":"ripgrep"},{"kind":"add-brew","name":"iterm2","cask":true}]`

func TestBatchProposalOneTerminalApproval(t *testing.T) {
	env, home := brewSetup(t)
	before := readFile(t, filepath.Join(env.Cwd, "Brewfile"))
	r := invoke(env, brewBatch, "propose", "batch", "-", "-json")
	if r.code != ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var p proposal.Proposal
	if err := json.Unmarshal([]byte(r.stdout), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Ops) != 3 || len(p.Base) != 1 || p.Tier != proposal.TierHigh || len(p.Reasons) != 1 || !strings.Contains(p.Reasons[0], "casks") || len(p.Plan) == 0 {
		t.Fatalf("incomplete combined proposal: %+v", p)
	}
	for _, want := range []string{`+brew "jq"`, `+brew "ripgrep"`, `+cask "iterm2"`} {
		if !strings.Contains(p.Diff, want) {
			t.Errorf("diff lacks %q: %s", want, p.Diff)
		}
	}
	all, err := (proposal.Store{Dir: proposal.Dir(home)}).List()
	if err != nil || len(all) != 1 {
		t.Fatalf("saved %d proposals: %v", len(all), err)
	}
	if readFile(t, filepath.Join(env.Cwd, "Brewfile")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Fatal("proposing changed the configuration")
	}

	env.StdinTTY = true
	r = invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitOK || strings.Count(r.stdout, "[y/N]") != 1 {
		t.Fatalf("exit %d: %s\n%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{`brew "jq"`, `brew "ripgrep"`, `cask "iterm2"`} {
		if !strings.Contains(readFile(t, filepath.Join(env.Cwd, "Brewfile")), want) {
			t.Errorf("approved Brewfile lacks %q", want)
		}
	}
	if gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "2" || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("approval did not commit the whole batch once")
	}
}

func TestBatchProposalReadsFile(t *testing.T) {
	env, _ := brewSetup(t)
	path := filepath.Join(t.TempDir(), "operations.json")
	if err := os.WriteFile(path, []byte(brewBatch), 0o600); err != nil {
		t.Fatal(err)
	}
	p := propose(t, env, "batch", path)
	if len(p.Ops) != 3 {
		t.Fatalf("ops = %+v", p.Ops)
	}
}

func TestBatchProposalRejectsWithoutSaving(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"empty", "[]", "no operations"},
		{"null", "null", "no operations"},
		{"object", "{}", "JSON array"},
		{"invalid operation", `[{"kind":"add-brew","name":"jq"},{"kind":"erase"}]`, "operation 2"},
		{"duplicate", `[{"kind":"add-brew","name":"jq"},{"kind":"add-brew","name":"jq"}]`, "conflicts"},
		{"conflicting pin", `[{"kind":"pin-mise","name":"go","version":"1"},{"kind":"pin-mise","name":"go","version":"2"}]`, "conflicts"},
		{"opposing rule", `[{"kind":"add-permission","decision":"deny","command":"git push"},{"kind":"remove-permission","decision":"deny","command":"git  push"}]`, "conflicts"},
		{"unknown field", `[{"kind":"add-brew","name":"jq","extra":true}]`, "unknown field"},
		{"trailing JSON", "[] []", "exactly one"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, home := brewSetup(t)
			r := invoke(env, tt.input, "propose", "batch", "-", "-json")
			if r.code != ExitUsage || !strings.Contains(r.stderr, tt.want) {
				t.Errorf("exit %d: %s, want %q", r.code, r.stderr, tt.want)
			}
			all, err := (proposal.Store{Dir: proposal.Dir(home)}).List()
			if err != nil || len(all) != 0 || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
				t.Errorf("rejected batch saved or changed state: %v, %d proposals", err, len(all))
			}
		})
	}
}

func TestBatchApprovalRefusesChangedBase(t *testing.T) {
	env, _ := brewSetup(t)
	p := batchFromStdin(t, env, brewBatch)
	path := filepath.Join(env.Cwd, "Brewfile")
	if err := os.WriteFile(path, []byte(`brew "git"
# edited
`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, env.Cwd, "commit", "-qam", "edit")
	env.StdinTTY = true
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "Brewfile changed since proposal") || strings.Contains(readFile(t, path), "jq") {
		t.Fatalf("exit %d: %s\n%s", r.code, r.stdout, r.stderr)
	}
}

func batchFromStdin(t *testing.T, env Env, input string) proposal.Proposal {
	t.Helper()
	r := invoke(env, input, "propose", "batch", "-", "-json")
	if r.code != ExitOK {
		t.Fatalf("batch exit %d: %s", r.code, r.stderr)
	}
	var p proposal.Proposal
	if err := json.Unmarshal([]byte(r.stdout), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

type failingBatchRunner struct{ brewRunner }

func (b failingBatchRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name == "brew" && len(c.Args) > 1 && c.Args[0] == "bundle" && c.Args[1] == "install" {
		return runner.Result{ExitCode: 1}, nil
	}
	return b.brewRunner.Run(ctx, c)
}

func TestBatchApprovalRestoresEveryConfigFile(t *testing.T) {
	env, home := brewSetup(t)
	env.Runner = failingBatchRunner{env.Runner.(brewRunner)}
	p := batchFromStdin(t, env, `[{"kind":"add-brew","name":"jq"},{"kind":"add-permission","decision":"review","command":"git push"}]`)
	beforeYAML := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	beforeBrew := readFile(t, filepath.Join(env.Cwd, "Brewfile"))
	env.StdinTTY = true

	r := invoke(env, "y\n", "approve", p.ID)
	if r.code != ExitErr || !strings.Contains(r.stderr, "restored the config files") {
		t.Fatalf("exit %d: %s\n%s", r.code, r.stdout, r.stderr)
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != beforeYAML || readFile(t, filepath.Join(env.Cwd, "Brewfile")) != beforeBrew || gitIn(t, env.Cwd, "status", "--porcelain") != "" || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
		t.Error("failed batch left config changes or committed")
	}
	if _, err := (proposal.Store{Dir: proposal.Dir(home)}).Load(p.ID); err != nil {
		t.Errorf("failed proposal should stay pending: %v", err)
	}
}

func TestMCPBatchCreatesCombinedProposal(t *testing.T) {
	env, home := brewSetup(t)
	c := startMCP(t, env, nil)
	var ops []proposal.Op
	if err := json.Unmarshal([]byte(brewBatch), &ops); err != nil {
		t.Fatal(err)
	}
	r := c.tool("zakwas_propose", map[string]any{"ops": ops}, nil)
	if r.isErr {
		t.Fatal(r.text)
	}
	p := parseJSON(t, r.text)
	if len(p["ops"].([]any)) != 3 || p["tier"] != "high" || !strings.Contains(p["next"].(string), "terminal") {
		t.Fatalf("incomplete batch result: %s", r.text)
	}
	if !strings.Contains(p["diff"].(string), `+cask "iterm2"`) || len(p["plan"].([]any)) == 0 || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Fatalf("missing combined review or changed config: %s", r.text)
	}
	all, err := (proposal.Store{Dir: proposal.Dir(home)}).List()
	if err != nil || len(all) != 1 {
		t.Fatalf("saved %d proposals: %v", len(all), err)
	}
	env.StdinTTY = true
	approved := invoke(env, "y\n", "approve", p["id"].(string))
	if approved.code != ExitOK {
		t.Fatalf("terminal batch approval: %s\n%s", approved.stdout, approved.stderr)
	}
}

func TestMCPBatchRejectsWithoutSaving(t *testing.T) {
	for _, tt := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty", map[string]any{"ops": []any{}}, "no operations"},
		{"null", map[string]any{"ops": nil}, "no operations"},
		{"invalid", map[string]any{"ops": []any{map[string]any{"kind": "add-brew", "name": "jq"}, map[string]any{"kind": "erase"}}}, "operation 2"},
		{"duplicate", map[string]any{"ops": []any{map[string]any{"kind": "add-brew", "name": "jq"}, map[string]any{"kind": "add-brew", "name": "jq"}}}, "conflicts"},
		{"mixed", map[string]any{"ops": []any{}, "kind": "add-brew", "name": "jq"}, "cannot be combined"},
		{"unknown", map[string]any{"ops": []any{map[string]any{"kind": "add-brew", "name": "jq", "extra": true}}}, "unknown field"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, home := brewSetup(t)
			c := startMCP(t, env, nil)
			r := c.tool("zakwas_propose", tt.args, nil)
			if !r.isErr || !strings.Contains(r.text, tt.want) {
				t.Errorf("result = %+v, want %q", r, tt.want)
			}
			all, err := (proposal.Store{Dir: proposal.Dir(home)}).List()
			if err != nil || len(all) != 0 || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
				t.Errorf("rejected MCP batch saved or changed state: %v", err)
			}
		})
	}
}

func TestMCPBatchLowRiskOneConfirmation(t *testing.T) {
	env, _ := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	r := c.tool("zakwas_propose", map[string]any{"ops": []proposal.Op{{Kind: proposal.AddBrew, Name: "jq"}, {Kind: proposal.AddBrew, Name: "ripgrep"}}}, nil)
	p := parseJSON(t, r.text)
	applied := c.tool("zakwas_apply", map[string]any{"id": p["id"]}, accept)
	if applied.isErr || len(applied.elicited) != 1 || parseJSON(t, applied.text)["committed"] != true {
		t.Fatalf("batch apply: %+v", applied)
	}
	if !strings.Contains(readFile(t, filepath.Join(env.Cwd, "Brewfile")), `brew "ripgrep"`) || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "2" {
		t.Error("batch was not applied in one commit")
	}
}

func TestMCPBatchApprovalRestoresBrewfile(t *testing.T) {
	env, home := brewSetup(t)
	env.Runner = failingBatchRunner{env.Runner.(brewRunner)}
	c := startMCP(t, env, formClient, "--enable-apply")
	r := c.tool("zakwas_propose", map[string]any{"ops": []proposal.Op{{Kind: proposal.AddBrew, Name: "jq"}, {Kind: proposal.AddBrew, Name: "ripgrep"}}}, nil)
	p := parseJSON(t, r.text)
	before := readFile(t, filepath.Join(env.Cwd, "Brewfile"))
	applied := c.tool("zakwas_apply", map[string]any{"id": p["id"]}, accept)
	if !applied.isErr || !strings.Contains(applied.text, "restored the config files") {
		t.Fatalf("failed batch: %+v", applied)
	}
	if readFile(t, filepath.Join(env.Cwd, "Brewfile")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
		t.Error("failed MCP batch left edits or committed")
	}
	if _, err := (proposal.Store{Dir: proposal.Dir(home)}).Load(p["id"].(string)); err != nil {
		t.Errorf("failed proposal should stay pending: %v", err)
	}
}
