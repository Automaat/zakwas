package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/proposal"
)

const captureConfig = `agents:
  guard: false
  providers: [claude, codex]
`

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureSetup is a config repo whose agents have been changed by hand: a
// marketplace and plugins, MCP servers, allow rules and approvals in both
// Claude and Codex, plus what capture must leave out.
func captureSetup(t *testing.T) (Env, string) {
	t.Helper()
	env, home := proposeSetupConfig(t, captureConfig)
	writeAt(t, filepath.Join(home, ".claude", "settings.json"), `{
  "extraKnownMarketplaces": {"sai": {"autoUpdate": true}},
  "permissions": {"allow": ["Bash(git status:*)", "Bash(ls *)", "Bash(npm run build)", "WebFetch(domain:example.com)"]}
}`)
	writeAt(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"),
		`{"sai": {"source": {"source": "github", "repo": "o/sai"}, "installLocation": "/x/sai", "autoUpdate": true}}`)
	writeAt(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {
  "docs": {"type": "stdio", "command": "npx", "args": ["-y", "docs-mcp"], "env": {"DOCS_TOKEN": "${DOCS_TOKEN}"}},
  "secret": {"type": "stdio", "command": "s", "env": {"KEY": "hunter2"}}
}}`)
	writeAt(t, filepath.Join(home, ".codex", "config.toml"), `
[marketplaces.cx]
source_type = "git"
source = "o/cx"

[mcp_servers.docs]
command = "npx"
args = ["-y", "docs-mcp"]
env_vars = ["DOCS_TOKEN"]
`)
	writeAt(t, filepath.Join(home, ".codex", "rules", "default.rules"),
		"prefix_rule(pattern=[\"git\", \"status\"], decision=\"allow\")\nprefix_rule(pattern=[\"cargo\", \"build\"], decision=\"allow\")\n")
	fake := env.Runner.(gitRunner).Fake
	fake.OnOK("claude plugin marketplace list --json", `[{"name":"sai","source":"github","repo":"o/sai"}]`)
	fake.OnOK("claude plugin list --json --available", `{"installed": [
		{"id":"humanize@sai","version":"1","scope":"user","enabled":true},
		{"id":"team@sai","version":"1","scope":"project","enabled":true},
		{"id":"cloud@synced","version":"1","scope":"user","enabled":true},
		{"id":"core@builtin","version":"1","scope":"user","enabled":true},
		{"id":"off@sai","version":"1","scope":"user","enabled":false}], "available": []}`)
	fake.OnOK("codex plugin marketplace list --json", `{"marketplaces":[{"name":"cx","root":"/y"}]}`)
	fake.OnOK("codex plugin list --available --json", `{"installed": [{"pluginId":"p@cx","version":"1","enabled":true}], "available": []}`)
	return env, home
}

func captureJSON(t *testing.T, env Env) captureDoc {
	t.Helper()
	r := invoke(env, "", "capture", "-json")
	if r.code != ExitOK {
		t.Fatalf("capture exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	var doc captureDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	return doc
}

func TestCaptureListsWithoutChangingAnything(t *testing.T) {
	env, home := captureSetup(t)
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	files := []string{
		filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".codex", "rules", "default.rules"),
	}
	snapshot := map[string]string{}
	for _, f := range files {
		snapshot[f] = readFile(t, f)
	}

	r := invoke(env, "", "capture")
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{
		"claude\n", "  marketplace  sai  o/sai\n", "  plugin       humanize@sai\n",
		"  mcp server   docs  stdio npx -y docs-mcp\n", "  allow rule   git status\n", "  allow rule   ls\n",
		"codex\n", "  marketplace  cx  o/cx\n", "  plugin       p@cx\n", "  approval     allow git status\n", "  approval     allow cargo build\n",
		"Not captured\n", "claude plugin off@sai: installed but disabled",
		"claude mcp server secret: has settings capture can't declare (env)", "claude allow rule Bash(...): matches one exact command",
		"claude allow rule WebFetch(...): not a Bash command rule",
		"Save these as one proposal with: zakwas capture --write",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	for _, never := range []string{"team@sai", "cloud@synced", "core@builtin", "hunter2"} {
		if strings.Contains(r.stdout, never) {
			t.Errorf("stdout lists %q:\n%s", never, r.stdout)
		}
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("capture edited the repo")
	}
	for f, body := range snapshot {
		if readFile(t, f) != body {
			t.Errorf("capture changed %s", f)
		}
	}
	if all, _ := (proposal.Store{Dir: proposal.Dir(home)}).List(); len(all) != 0 {
		t.Errorf("capture without --write saved %d proposals", len(all))
	}
	for _, l := range env.Runner.(gitRunner).Lines() {
		if !strings.Contains(l, " list ") && !strings.Contains(l, "marketplace list") {
			t.Errorf("capture ran %q", l)
		}
	}
}

func TestCaptureJSON(t *testing.T) {
	env, _ := captureSetup(t)
	doc := captureJSON(t, env)
	if doc.FormatVersion != engine.FormatVersion || !strings.HasSuffix(doc.Config, "zakwas.yaml") || doc.Proposal != nil {
		t.Errorf("doc = %+v", doc)
	}
	var got []string
	for _, op := range doc.Ops {
		got = append(got, op.Kind+" "+op.Name+op.Command+" "+strings.Join(op.Providers, ","))
	}
	want := []string{
		"add-marketplace sai claude", "add-marketplace cx codex",
		"add-plugin humanize@sai claude", "add-plugin p@cx codex",
		"add-mcp docs claude,codex",
		"add-permission git status claude,codex", "add-permission ls claude", "add-permission cargo build codex",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ops\n got %q\nwant %q", got, want)
	}
	if len(doc.Findings) != 10 || len(doc.Skipped) != 4 {
		t.Errorf("%d findings, %d skipped", len(doc.Findings), len(doc.Skipped))
	}
	for _, c := range doc.Skipped {
		if c.Skip == "" {
			t.Errorf("skipped without a reason: %+v", c)
		}
	}
}

func TestCaptureWriteSavesOneProposalThatApprovalConverges(t *testing.T) {
	env, home := captureSetup(t)
	r := invoke(env, "", "capture", "--write")
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	all, err := (proposal.Store{Dir: proposal.Dir(home)}).List()
	if err != nil || len(all) != 1 {
		t.Fatalf("proposals = %d, %v", len(all), err)
	}
	p := all[0]
	if p.Tier != proposal.TierHigh || len(p.Ops) != 8 || !strings.Contains(p.Summary, "add marketplace sai") {
		t.Errorf("tier %s, %d ops, summary %q", p.Tier, len(p.Ops), p.Summary)
	}
	for _, want := range []string{"Proposal " + p.ID, "(high risk)", "zakwas approve " + p.ID, "Nothing was changed", "Not captured"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != captureConfig || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("--write edited the repo")
	}

	env.StdinTTY = true
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("approve exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	yaml := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	for _, want := range []string{"marketplaces:\n    sai:\n      source: o/sai\n      providers: [claude]\n", "- id: p@cx\n      providers: [codex]", "docs:\n      command: npx", "- command: git status\n        providers: [claude, codex]"} {
		if !strings.Contains(yaml, want) {
			t.Errorf("zakwas.yaml lacks %q:\n%s", want, yaml)
		}
	}

	doc := captureJSON(t, env)
	if len(doc.Findings) != 0 || len(doc.Ops) != 0 {
		t.Errorf("a second capture lists %+v", doc.Findings)
	}
	if len(doc.Skipped) != 4 {
		t.Errorf("skipped = %d, want the 4 that can't be declared", len(doc.Skipped))
	}
	r = invoke(env, "", "capture")
	if r.code != ExitOK || !strings.Contains(r.stdout, "Nothing to capture") || strings.Contains(r.stdout, "--write") {
		t.Errorf("second capture:\n%s%s", r.stdout, r.stderr)
	}
	if r := invoke(env, "", "capture", "--write"); r.code != ExitOK || strings.Contains(r.stdout, "Proposal") {
		t.Errorf("second capture --write:\n%s%s", r.stdout, r.stderr)
	}
	if all, _ := (proposal.Store{Dir: proposal.Dir(home)}).List(); len(all) != 0 {
		t.Errorf("proposals left = %d", len(all))
	}
	if r := invoke(env, "", "check", "--only", "agents"); r.code != ExitOK {
		t.Errorf("the agents drifted after approval: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestCaptureSkipsWhatTheConfigCannotTake(t *testing.T) {
	env, _ := captureSetup(t)
	writeAt(t, filepath.Join(env.Cwd, "zakwas.yaml"), captureConfig+"  marketplaces:\n    sai:\n      source: o/sai\n      providers: [codex]\n")
	gitIn(t, env.Cwd, "commit", "-q", "-am", "declare sai")
	doc := captureJSON(t, env)
	skipped := map[string]string{}
	for _, c := range doc.Skipped {
		skipped[c.Kind+" "+c.Name] = c.Skip
	}
	if why := skipped["marketplace sai"]; !strings.Contains(why, "other providers only") {
		t.Errorf("marketplace: %q", why)
	}
	if why := skipped["plugin humanize@sai"]; !strings.Contains(why, `"sai"`) {
		t.Errorf("plugin: %q", why)
	}
	for _, op := range doc.Ops {
		if op.Kind == proposal.AddPlugin && op.Name == "humanize@sai" {
			t.Error("a plugin without a marketplace is proposed")
		}
	}
}

func TestCaptureMergesOnlyMatchingProviders(t *testing.T) {
	env, home := captureSetup(t)
	writeAt(t, filepath.Join(home, ".codex", "config.toml"), `
[marketplaces.sai]
source_type = "git"
source = "o/another"

[mcp_servers.docs]
command = "npx"
args = ["-y", "other-docs"]
`)
	fake := env.Runner.(gitRunner).Fake
	fake.OnOK("codex plugin marketplace list --json", `{"marketplaces":[{"name":"sai","root":"/y"}]}`)
	fake.OnOK("codex plugin list --available --json", `{"installed": [], "available": []}`)
	doc := captureJSON(t, env)
	skipped := map[string]string{}
	for _, c := range doc.Skipped {
		skipped[c.Provider+" "+c.Kind+" "+c.Name] = c.Skip
	}
	if why := skipped["codex marketplace sai"]; !strings.Contains(why, "comes from o/another") {
		t.Errorf("marketplace: %q", why)
	}
	if why := skipped["codex mcp docs"]; !strings.Contains(why, "differs from claude's server") {
		t.Errorf("mcp: %q", why)
	}
	for _, op := range doc.Ops {
		if (op.Kind == proposal.AddMarketplace || op.Kind == proposal.AddMCP) && !slices.Equal(op.Providers, []string{"claude"}) {
			t.Errorf("%s %s is proposed for %v", op.Kind, op.Name, op.Providers)
		}
	}
}

func TestCaptureNeedsAnAgentsSection(t *testing.T) {
	env, _ := proposeSetup(t)
	writeAt(t, filepath.Join(env.Cwd, "zakwas.yaml"), "brew: {file: Brewfile}\n")
	r := invoke(env, "", "capture")
	if r.code != ExitErr || !strings.Contains(r.stderr, "no agents section") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestCaptureReportsAProviderItCannotRead(t *testing.T) {
	env, _ := captureSetup(t)
	gr := env.Runner.(gitRunner)
	gr.Missing("claude")
	r := invoke(env, "", "capture")
	if r.code != ExitErr || !strings.Contains(r.stderr, "Claude Code is not installed") || !strings.Contains(r.stdout, "approval     allow git status") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestCaptureRejectsArguments(t *testing.T) {
	env, _ := captureSetup(t)
	if r := invoke(env, "", "capture", "extra"); r.code != ExitUsage || !strings.Contains(r.stderr, "Usage: zakwas capture") {
		t.Errorf("exit %d\n%s", r.code, r.stderr)
	}
	if r := invoke(env, "", "capture", "-h"); r.code != ExitOK || !strings.Contains(r.stdout, "--write") {
		t.Errorf("exit %d\n%s", r.code, r.stdout)
	}
}

func TestMCPCaptureIsReadOnly(t *testing.T) {
	env, home := captureSetup(t)
	c := startMCP(t, env, formClient)
	tools := c.toolNames()
	if tools["zakwas_capture"] == nil || tools["zakwas_capture"]["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatalf("zakwas_capture = %v", tools["zakwas_capture"])
	}
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	res := c.tool("zakwas_capture", map[string]any{}, nil)
	if res.isErr {
		t.Fatalf("zakwas_capture failed: %s", res.text)
	}
	doc := parseJSON(t, res.text)
	if doc["format_version"] != engine.FormatVersion || len(doc["findings"].([]any)) != 10 || len(doc["ops"].([]any)) != 8 || len(doc["skipped"].([]any)) != 4 {
		t.Errorf("doc = %s", res.text)
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("the scan edited the repo")
	}
	if all, _ := (proposal.Store{Dir: proposal.Dir(home)}).List(); len(all) != 0 {
		t.Errorf("the scan saved %d proposals", len(all))
	}

	marketplace := doc["ops"].([]any)[0].(map[string]any)
	if marketplace["kind"] != "add-marketplace" {
		t.Fatalf("first op = %v", marketplace)
	}
	p := parseJSON(t, c.tool("zakwas_propose", marketplace, nil).text)
	if p["tier"] != "high" || !strings.Contains(p["summary"].(string), "add marketplace sai") {
		t.Errorf("proposal = %v", p)
	}
}

func TestCaptureIsListedWithTheCommands(t *testing.T) {
	var found bool
	for _, c := range commandList {
		found = found || c.name == "capture"
	}
	if !found || !strings.Contains(usage(), "capture [--write]") {
		t.Error("capture is missing from the command list")
	}
}

func TestCaptureConvergesWhenAPluginIsInstalledWhereTheMarketplacesDiffer(t *testing.T) {
	env, home := proposeSetupConfig(t, captureConfig)
	writeAt(t, filepath.Join(home, ".claude", "settings.json"), `{"extraKnownMarketplaces": {"diff": {"autoUpdate": true}}}`)
	writeAt(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"),
		`{"diff": {"source": {"source": "github", "repo": "o/diffA"}, "installLocation": "/x/diff", "autoUpdate": true}}`)
	writeAt(t, filepath.Join(home, ".codex", "config.toml"), "[marketplaces.diff]\nsource_type = \"git\"\nsource = \"o/diffB\"\n")
	fake := env.Runner.(gitRunner).Fake
	fake.OnOK("claude plugin marketplace list --json", `[{"name":"diff","source":"github","repo":"o/diffA"}]`)
	fake.OnOK("claude plugin list --json --available", `{"installed": [{"id":"q@diff","version":"1","scope":"user","enabled":true}], "available": []}`)
	fake.OnOK("codex plugin marketplace list --json", `{"marketplaces":[{"name":"diff","root":"/y"}]}`)
	fake.OnOK("codex plugin list --available --json", `{"installed": [{"pluginId":"q@diff","version":"1","enabled":true}], "available": []}`)

	doc := captureJSON(t, env)
	var got []string
	for _, op := range doc.Ops {
		got = append(got, op.Kind+" "+op.Name+" "+strings.Join(op.Providers, ","))
	}
	if want := []string{"add-marketplace diff claude", "add-plugin q@diff claude"}; !slices.Equal(got, want) {
		t.Fatalf("ops\n got %q\nwant %q", got, want)
	}
	if r := invoke(env, "", "capture", "--write"); r.code != ExitOK {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	all, _ := (proposal.Store{Dir: proposal.Dir(home)}).List()
	if len(all) != 1 {
		t.Fatalf("proposals = %d", len(all))
	}
	env.StdinTTY = true
	if r := invoke(env, "y\n", "approve", all[0].ID); r.code != ExitOK {
		t.Fatalf("approve exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if doc := captureJSON(t, env); len(doc.Findings) != 0 || len(doc.Ops) != 0 {
		t.Errorf("a second capture lists %+v", doc.Findings)
	}
}

func TestFailedApproveRestoresTheOwnershipRecords(t *testing.T) {
	env, home := proposeSetupConfig(t, "agents:\n  guard: false\n  providers: [claude]\n  marketplaces:\n    sai: o/sai\n")
	writeAt(t, filepath.Join(home, ".claude", "settings.json"), `{"extraKnownMarketplaces": {"sai": {"autoUpdate": true}}}`)
	writeAt(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"),
		`{"sai": {"source": {"source": "github", "repo": "o/sai"}, "installLocation": "/x/sai", "autoUpdate": true}}`)
	handAdded := `{"mcpServers": {"docs": {"type": "stdio", "command": "npx", "args": ["-y", "docs-mcp"]}}}`
	writeAt(t, filepath.Join(home, ".claude.json"), handAdded)
	fake := env.Runner.(gitRunner).Fake
	fake.OnOK("claude plugin marketplace list --json", `[{"name":"sai","source":"github","repo":"o/sai"}]`)
	fake.OnOK("claude plugin list --json --available", `{"installed": [], "available": [{"pluginId":"x@sai","version":"1"}]}`)

	cfg, err := loadConfig("", env.Cwd, env.Home)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := saveProposal(context.Background(), env, cfg,
		proposal.Op{Kind: proposal.AddMCP, Name: "docs", Server: &proposal.Server{Command: "npx", Args: []string{"-y", "docs-mcp"}}, Providers: []string{"claude"}},
		proposal.Op{Kind: proposal.AddPlugin, Name: "x@sai", Providers: []string{"claude"}})
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	env.StdinTTY = true
	r := invoke(env, "y\n", "approve", p.ID)
	if r.code == ExitOK {
		t.Fatalf("approve succeeded:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "restored the config files") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before {
		t.Error("the config wasn't restored")
	}
	if _, err := os.Stat(agents.MCPStatePath(home)); err == nil {
		t.Errorf("the failed approve left ownership of docs behind:\n%s", readFile(t, agents.MCPStatePath(home)))
	}
	if readFile(t, filepath.Join(home, ".claude.json")) != handAdded {
		t.Error("the hand-added server changed")
	}
	if r := invoke(env, "", "plan", "--only", "agents"); strings.Contains(r.stdout, "- claude mcp docs") {
		t.Errorf("zakwas plans to remove the hand-added server:\n%s", r.stdout)
	}
}
