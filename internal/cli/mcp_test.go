package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/lock"
	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner"
)

type mcpClient struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	stderr *lockedBuffer
	done   chan struct{}
	code   int
	nextID int
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var formClient = map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}

func startMCP(t *testing.T, env Env, caps map[string]any, args ...string) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &mcpClient{t: t, in: inW, out: bufio.NewReader(outR), stderr: &lockedBuffer{}, done: make(chan struct{})}
	env.Args = append([]string{"mcp"}, args...)
	env.Stdin, env.Stdout, env.Stderr = inR, outW, c.stderr
	go func() {
		c.code = Main(context.Background(), env)
		_ = outW.Close()
		close(c.done)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		go func() { _, _ = io.Copy(io.Discard, outR) }()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("zakwas mcp did not stop when stdin closed")
		}
	})
	c.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": caps, "clientInfo": map[string]any{"name": "test"}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return c
}

func (c *mcpClient) send(v any) {
	c.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *mcpClient) readLine() map[string]any {
	c.t.Helper()
	type read struct {
		line string
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		line, err := c.out.ReadString('\n')
		ch <- read{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("reading from zakwas mcp: %v\nstderr: %s", r.err, c.stderr)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.line), &m); err != nil {
			c.t.Fatalf("not JSON: %q", r.line)
		}
		return m
	case <-time.After(30 * time.Second):
		c.t.Fatalf("timed out\nstderr: %s", c.stderr)
		return nil
	}
}

func (c *mcpClient) request(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	for {
		m := c.readLine()
		if m["id"] == float64(c.nextID) && m["method"] == nil {
			return m
		}
	}
}

type toolCall struct {
	text     string
	isErr    bool
	elicited []string
	progress []string
}

func decline(string) map[string]any {
	return map[string]any{"result": map[string]any{"action": "decline"}}
}

func accept(string) map[string]any {
	return map[string]any{"result": map[string]any{"action": "accept", "content": map[string]any{"apply": true}}}
}

func (c *mcpClient) tool(name string, args any, answer func(msg string) map[string]any) toolCall {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
		"name": name, "arguments": args, "_meta": map[string]any{"progressToken": "p"},
	}})
	var got toolCall
	for {
		m := c.readLine()
		switch {
		case m["method"] == "elicitation/create":
			msg := m["params"].(map[string]any)["message"].(string)
			got.elicited = append(got.elicited, msg)
			reply := answer(msg)
			reply["jsonrpc"], reply["id"] = "2.0", m["id"]
			c.send(reply)
		case m["method"] == "notifications/progress":
			got.progress = append(got.progress, m["params"].(map[string]any)["message"].(string))
		case m["id"] == float64(id):
			res, ok := m["result"].(map[string]any)
			if !ok {
				c.t.Fatalf("%s failed: %v", name, m["error"])
			}
			got.text = res["content"].([]any)[0].(map[string]any)["text"].(string)
			got.isErr = res["isError"].(bool)
			return got
		}
	}
}

func (c *mcpClient) toolNames() map[string]map[string]any {
	c.t.Helper()
	out := map[string]map[string]any{}
	for _, tool := range c.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func parseJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	return m
}

const brewConfig = proposeConfig + "brew: {file: Brewfile}\n"

// brewRunner reports jq missing while the Brewfile names it, so a proposal
// that adds it has a step to apply.
type brewRunner struct {
	gitRunner
	install func(ctx context.Context)
}

func (b brewRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name != "brew" {
		return b.gitRunner.Run(ctx, c)
	}
	if len(c.Args) > 3 && c.Args[0] == "bundle" && c.Args[1] == "check" {
		if data, err := os.ReadFile(c.Args[3]); err == nil && bytes.Contains(data, []byte("jq")) {
			return runner.Result{ExitCode: 1, Stdout: "→ Formula jq needs to be installed.\n"}, nil
		}
	}
	if len(c.Args) > 1 && c.Args[0] == "bundle" && c.Args[1] == "install" && b.install != nil {
		b.install(ctx)
		if ctx.Err() != nil {
			return runner.Result{}, ctx.Err()
		}
	}
	return runner.Result{}, nil
}

func brewSetup(t *testing.T) (Env, string) {
	t.Helper()
	env, home := proposeSetupConfig(t, brewConfig)
	env.Runner = brewRunner{gitRunner: env.Runner.(gitRunner)}
	return env, home
}

func TestMCPServesTheReadAndProposeTools(t *testing.T) {
	env, _ := proposeSetup(t)
	c := startMCP(t, env, formClient)

	tools := c.toolNames()
	for _, name := range []string{"zakwas_config", "zakwas_plan", "zakwas_check", "zakwas_propose", "zakwas_proposals", "zakwas_discard"} {
		if tools[name] == nil {
			t.Errorf("no tool %s in %v", name, tools)
		}
	}
	if tools["zakwas_apply"] != nil {
		t.Error("zakwas_apply is offered without --enable-apply")
	}
	for _, name := range []string{"zakwas_config", "zakwas_plan", "zakwas_check", "zakwas_proposals"} {
		if tools[name]["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Errorf("%s isn't marked read-only", name)
		}
	}

	cfg := parseJSON(t, c.tool("zakwas_config", map[string]any{}, nil).text)
	if !strings.HasSuffix(cfg["config"].(string), "zakwas.yaml") || cfg["guard"] != false || !strings.Contains(cfg["yaml"].(string), "agents:") {
		t.Errorf("config = %v", cfg)
	}
	if mods := cfg["modules"].([]any); len(mods) == 0 {
		t.Errorf("no modules in %v", cfg)
	}

	plan := c.tool("zakwas_plan", map[string]any{"only": []string{"templates"}}, nil)
	if doc := parseJSON(t, plan.text); plan.isErr || doc["format_version"] != "1" || len(doc["modules"].([]any)) != 1 {
		t.Errorf("plan (error %v) = %s", plan.isErr, plan.text)
	}
	check := parseJSON(t, c.tool("zakwas_check", map[string]any{}, nil).text)
	if check["drift"] != true {
		t.Errorf("check = %v, want drift: the opencode.json isn't rendered", check)
	}
	if bad := c.tool("zakwas_plan", map[string]any{"only": []string{"nope"}}, nil); !bad.isErr || !strings.Contains(bad.text, `"nope"`) {
		t.Errorf("unknown module gave %q", bad.text)
	}
}

func TestMCPProposeListsAndDiscards(t *testing.T) {
	env, home := proposeSetup(t)
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))
	c := startMCP(t, env, formClient)

	high := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-permission", "decision": "review", "command": "git push"}, nil).text)
	if high["tier"] != "high" || !strings.Contains(high["next"].(string), "zakwas approve "+high["id"].(string)) || !strings.Contains(high["next"].(string), "terminal") {
		t.Errorf("high proposal = %v", high)
	}
	mcpServer := map[string]any{"kind": "add-mcp", "name": "docs", "server": map[string]any{"command": "npx", "args": []string{"-y", "docs-mcp"}}}
	if p := parseJSON(t, c.tool("zakwas_propose", mcpServer, nil).text); p["tier"] != "high" {
		t.Errorf("an MCP server proposal is %v", p["tier"])
	}
	oauthServer := map[string]any{"kind": "add-mcp", "name": "slack", "server": map[string]any{
		"url": "https://mcp.example.com/slack", "oauth": map[string]any{"client_id": "0oa1abcdEXAMPLE", "callback_port": 54661, "scope": "read write"},
	}}
	if p := parseJSON(t, c.tool("zakwas_propose", oauthServer, nil).text); p["tier"] != "high" || !strings.Contains(p["diff"].(string), "oauth: {clientId: 0oa1abcdEXAMPLE, callbackPort: 54661, scope: read write}") {
		t.Errorf("an MCP server proposal with an OAuth client is %v", p)
	}
	for _, bad := range []map[string]any{
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "oauth": map[string]any{"callback_port": 1}}},
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "oauth": map[string]any{"client_id": "c", "client_secret": "LEAKP1"}}},
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "oauth": map[string]any{"client_id": "c", "callback_port": 0.5}}},
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "oauth": map[string]any{"client_id": "c", "callback_port": 0}}},
	} {
		if r := c.tool("zakwas_propose", bad, nil); !r.isErr || strings.Contains(r.text, "LEAKP1") {
			t.Errorf("%v: error %v, text %s", bad, r.isErr, r.text)
		}
	}
	for _, bad := range []map[string]any{
		{"kind": "add-brew", "name": "jq", "ghp_LEAKKEY": true},
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "ghp_LEAKKEY": "x"}},
		{"kind": "add-mcp", "name": "a", "server": map[string]any{"url": "https://e.com", "oauth": map[string]any{"client_id": "c", "ghp_LEAKKEY": "x"}}},
	} {
		r := c.tool("zakwas_propose", bad, nil)
		if !r.isErr || strings.Contains(r.text, "LEAKKEY") || !strings.Contains(r.text, "unknown field") {
			t.Errorf("%v: error %v, text %s", bad, r.isErr, r.text)
		}
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before || gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("proposing edited the repo")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
		t.Error("proposing changed the system")
	}

	list := parseJSON(t, c.tool("zakwas_proposals", map[string]any{}, nil).text)["proposals"].([]any)
	if len(list) != 3 {
		t.Fatalf("proposals = %v", list)
	}

	for _, bad := range []map[string]any{
		{"kind": "add-plugin", "name": "x@nowhere"},
		{"kind": "rm-rf"},
		{"kind": "add-brew", "name": "jq", "bogus": true},
		{"kind": "add-permission", "decision": "maybe", "command": "ls"},
	} {
		if r := c.tool("zakwas_propose", bad, nil); !r.isErr {
			t.Errorf("%v was accepted: %s", bad, r.text)
		}
	}

	id := high["id"].(string)
	if r := c.tool("zakwas_discard", map[string]any{"id": id}, nil); r.isErr || !strings.Contains(r.text, id) {
		t.Errorf("discard: %v %s", r.isErr, r.text)
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(home), id+".json")); err == nil {
		t.Error("discarded proposal is still there")
	}
	if r := c.tool("zakwas_discard", map[string]any{"id": id}, nil); !r.isErr {
		t.Error("discarded a proposal twice")
	}
	if list := parseJSON(t, c.tool("zakwas_proposals", map[string]any{}, nil).text)["proposals"].([]any); len(list) != 2 {
		t.Errorf("proposals after discard = %v", list)
	}
}

func TestMCPDiscardIgnoresOtherConfigs(t *testing.T) {
	env, home := proposeSetup(t)
	other := &proposal.Proposal{
		FormatVersion: "1", ID: "deadbeef", Created: env.now(), Config: "/elsewhere/zakwas.yaml", Summary: "x", Tier: "low",
	}
	if err := (proposal.Store{Dir: proposal.Dir(home)}).Save(other); err != nil {
		t.Fatal(err)
	}
	c := startMCP(t, env, formClient)
	if r := c.tool("zakwas_discard", map[string]any{"id": "deadbeef"}, nil); !r.isErr || !strings.Contains(r.text, "was made for /elsewhere/zakwas.yaml") {
		t.Errorf("discard of another config's proposal: %v %s", r.isErr, r.text)
	}
	if list := parseJSON(t, c.tool("zakwas_proposals", map[string]any{}, nil).text)["proposals"].([]any); len(list) != 0 {
		t.Errorf("another config's proposal is listed: %v", list)
	}
}

func TestMCPApplyIsOfferedOnlyWithTheFlag(t *testing.T) {
	env, _ := proposeSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	apply := c.toolNames()["zakwas_apply"]
	if apply == nil {
		t.Fatal("zakwas_apply isn't offered with --enable-apply")
	}
	if apply["_meta"].(map[string]any)["anthropic/requiresUserInteraction"] != true {
		t.Errorf("zakwas_apply doesn't require user interaction: %v", apply)
	}

	off := startMCP(t, env, formClient)
	resp := off.request("tools/call", map[string]any{"name": "zakwas_apply", "arguments": map[string]any{"id": "deadbeef"}})
	if resp["error"] == nil {
		t.Errorf("zakwas_apply ran without the flag: %v", resp)
	}
}

func TestMCPApplyAppliesAfterConfirmation(t *testing.T) {
	env, home := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	p := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)
	if p["tier"] != "low" || !strings.Contains(p["next"].(string), "zakwas_apply") {
		t.Fatalf("proposal = %v", p)
	}
	id := p["id"].(string)

	var busy error
	answer := func(string) map[string]any {
		l, err := lock.Acquire(context.Background(), home, "other", lock.Options{NoWait: true})
		if err == nil {
			_ = l.Release()
		}
		busy = err
		return accept("")
	}
	r := c.tool("zakwas_apply", map[string]any{"id": id}, answer)

	if r.isErr {
		t.Fatalf("apply failed: %s\nstderr: %s", r.text, c.stderr)
	}
	out := parseJSON(t, r.text)
	if out["applied"] != true || !strings.Contains(out["output"].(string), "not pushed") {
		t.Errorf("outcome = %v", out)
	}
	var busyErr *lock.BusyError
	if !errors.As(busy, &busyErr) {
		t.Errorf("the run lock wasn't held while asking: %v", busy)
	}
	if len(r.elicited) != 1 {
		t.Fatalf("asked %d times", len(r.elicited))
	}
	for _, want := range []string{"Proposal " + id, "(low risk)", `+brew "jq"`, "Approve " + id + ": apply", "commit Brewfile to main?"} {
		if !strings.Contains(r.elicited[0], want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, r.elicited[0])
		}
	}
	if len(r.progress) < 2 || !strings.Contains(r.progress[0], "[1/") || !strings.Contains(r.progress[len(r.progress)-1], "done") {
		t.Errorf("progress = %q", r.progress)
	}
	if got := readFile(t, filepath.Join(env.Cwd, "Brewfile")); !strings.Contains(got, `brew "jq"`) {
		t.Errorf("Brewfile:\n%s", got)
	}
	if status := gitIn(t, env.Cwd, "status", "--porcelain"); status != "" {
		t.Errorf("repo is dirty: %s", status)
	}
	if files := gitIn(t, env.Cwd, "show", "--name-only", "--format=", "HEAD"); files != "Brewfile" {
		t.Errorf("commit holds %q", files)
	}
	if _, err := os.Stat(HistoryPath(home)); err != nil {
		t.Errorf("apply wasn't recorded in the history: %v", err)
	}
	if l, err := lock.Acquire(context.Background(), home, "after", lock.Options{NoWait: true}); err != nil {
		t.Errorf("the run lock is still held: %v", err)
	} else {
		_ = l.Release()
	}
	if list := parseJSON(t, c.tool("zakwas_proposals", map[string]any{}, nil).text)["proposals"].([]any); len(list) != 0 {
		t.Errorf("applied proposal is still listed: %v", list)
	}
}

func TestMCPApplyNeedsAnExplicitYes(t *testing.T) {
	answers := map[string]func(string) map[string]any{
		"decline": decline,
		"cancel":  func(string) map[string]any { return map[string]any{"result": map[string]any{"action": "cancel"}} },
		"accepted without the box": func(string) map[string]any {
			return map[string]any{"result": map[string]any{"action": "accept", "content": map[string]any{"apply": false}}}
		},
		"accepted with a string": func(string) map[string]any {
			return map[string]any{"result": map[string]any{"action": "accept", "content": map[string]any{"apply": "true"}}}
		},
		"accepted with no content": func(string) map[string]any { return map[string]any{"result": map[string]any{"action": "accept"}} },
		"client error": func(string) map[string]any {
			return map[string]any{"error": map[string]any{"code": -32601, "message": "no elicitation here"}}
		},
		"malformed": func(string) map[string]any { return map[string]any{"result": "yes"} },
	}
	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			env, home := brewSetup(t)
			c := startMCP(t, env, formClient, "--enable-apply")
			id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
			brewfile := readFile(t, filepath.Join(env.Cwd, "Brewfile"))

			r := c.tool("zakwas_apply", map[string]any{"id": id}, answer)

			out := parseJSON(t, r.text)
			if out["applied"] == true || out["command"] != "zakwas approve "+id {
				t.Errorf("outcome = %v", out)
			}
			if readFile(t, filepath.Join(env.Cwd, "Brewfile")) != brewfile || gitIn(t, env.Cwd, "status", "--porcelain") != "" || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
				t.Error("the repo changed")
			}
			if _, err := os.Stat(HistoryPath(home)); err == nil {
				t.Error("something was applied")
			}
			if list := parseJSON(t, c.tool("zakwas_proposals", map[string]any{}, nil).text)["proposals"].([]any); len(list) != 1 {
				t.Errorf("the proposal is gone: %v", list)
			}
		})
	}
}

func TestMCPApplyNeverAppliesHighRisk(t *testing.T) {
	env, home := proposeSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	p := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-permission", "decision": "review", "command": "git push"}, nil).text)
	id := p["id"].(string)
	if p["tier"] != "high" {
		t.Fatalf("proposal = %v", p)
	}
	before := readFile(t, filepath.Join(env.Cwd, "zakwas.yaml"))

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	out := parseJSON(t, r.text)
	if out["applied"] == true || out["command"] != "zakwas approve "+id || !strings.Contains(out["reason"].(string), "terminal") {
		t.Errorf("outcome = %v", out)
	}
	if len(r.elicited) != 0 {
		t.Errorf("the user was asked about a high risk proposal: %q", r.elicited)
	}
	if readFile(t, filepath.Join(env.Cwd, "zakwas.yaml")) != before {
		t.Error("zakwas.yaml changed")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "opencode", "opencode.json")); err == nil {
		t.Error("the system changed")
	}
}

func TestMCPApplyRatesTheProposalAgainNotFromItsFile(t *testing.T) {
	env, home := proposeSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-permission", "decision": "review", "command": "git push"}, nil).text)["id"].(string)

	path := filepath.Join(proposal.Dir(home), id+".json")
	forged := strings.Replace(readFile(t, path), `"tier": "high"`, `"tier": "low"`, 1)
	if err := os.WriteFile(path, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)
	if out := parseJSON(t, r.text); out["applied"] == true || len(r.elicited) != 0 {
		t.Errorf("a proposal file claiming low risk was applied: %v", out)
	}
}

func TestMCPApplyWithoutElicitationGivesTheCommand(t *testing.T) {
	for name, caps := range map[string]map[string]any{
		"no capability": {},
		"url mode only": {"elicitation": map[string]any{"url": map[string]any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			env, _ := brewSetup(t)
			c := startMCP(t, env, caps, "--enable-apply")
			id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
			brewfile := readFile(t, filepath.Join(env.Cwd, "Brewfile"))

			r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

			out := parseJSON(t, r.text)
			if r.isErr || out["applied"] == true || out["command"] != "zakwas approve "+id {
				t.Errorf("outcome = %v", out)
			}
			if readFile(t, filepath.Join(env.Cwd, "Brewfile")) != brewfile || len(r.elicited) != 0 {
				t.Error("something was applied or asked")
			}
		})
	}
}

func TestMCPApplyRefusesWhenTheLockIsBusy(t *testing.T) {
	env, home := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	held, err := lock.Acquire(context.Background(), home, "zakwas apply", lock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	if !r.isErr || !strings.Contains(r.text, "another run is in progress") || len(r.elicited) != 0 {
		t.Errorf("busy lock gave (error %v) %q, asked %q", r.isErr, r.text, r.elicited)
	}
}

func TestMCPApplyDeclinedWithoutARunLockStillSaysSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	env, home := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	stateDir := filepath.Dir(lock.Path(home))
	if err := os.Chmod(stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	r := c.tool("zakwas_apply", map[string]any{"id": id}, decline)

	out := parseJSON(t, r.text)
	if out["applied"] == true || out["reason"] != "the user declined" || out["command"] != "zakwas approve "+id {
		t.Errorf("outcome = %v", out)
	}
}

func TestMCPApplyRefusesAnUnopenableLockFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	env, home := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	if err := os.WriteFile(lock.Path(home), nil, 0); err != nil {
		t.Fatal(err)
	}

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	if !r.isErr || !strings.Contains(r.text, "can't open the run lock") || len(r.elicited) != 0 {
		t.Errorf("unopenable lock gave (error %v) %q, asked %q", r.isErr, r.text, r.elicited)
	}
}

func TestMCPApplyKeepsAnEditSavedWhileItAsks(t *testing.T) {
	env, _ := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	brewfile := filepath.Join(env.Cwd, "Brewfile")

	r := c.tool("zakwas_apply", map[string]any{"id": id}, func(string) map[string]any {
		if err := os.WriteFile(brewfile, []byte("brew \"git\"\nbrew \"mine\"\n"), 0o644); err != nil {
			t.Error(err)
		}
		return accept("")
	})

	if out := parseJSON(t, r.text); out["applied"] == true {
		t.Errorf("applied over a file changed while asking: %v", out)
	}
	if got := readFile(t, brewfile); got != "brew \"git\"\nbrew \"mine\"\n" {
		t.Errorf("Brewfile was overwritten:\n%s", got)
	}
}

func TestMCPConfirmationShowsOnlyPlainText(t *testing.T) {
	env, _ := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)

	r := c.tool("zakwas_apply", map[string]any{"id": id}, decline)

	if len(r.elicited) != 1 || strings.ContainsAny(r.elicited[0], "\x1b\r\x00") {
		t.Errorf("the confirmation has control characters: %q", r.elicited)
	}
	if got := plainText("a\x1b[31mb\r\nc\td"); got != "a[31mb\nc\td" {
		t.Errorf("plainText = %q", got)
	}
}

func TestMCPStopsWhenStdinCloses(t *testing.T) {
	env, _ := proposeSetup(t)
	c := startMCP(t, env, formClient)
	_ = c.in.Close()
	select {
	case <-c.done:
		if c.code != ExitOK {
			t.Errorf("exit %d\n%s", c.code, c.stderr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop")
	}
}

func TestMCPRejectsArguments(t *testing.T) {
	env, _ := proposeSetup(t)
	r := invoke(env, "", "mcp", "extra")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "Usage: zakwas mcp") {
		t.Errorf("exit %d\n%s", r.code, r.stderr)
	}
	r = invoke(env, "", "mcp", "-h")
	if r.code != ExitOK || !strings.Contains(r.stdout, "--enable-apply") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestMCPLeavesTheConfigToEachCall(t *testing.T) {
	t.Setenv("ZAKWAS_CONFIG", "")
	env, _ := proposeSetup(t)
	env.Cwd = t.TempDir()
	c := startMCP(t, env, formClient)
	if r := c.tool("zakwas_config", map[string]any{}, nil); !r.isErr || !strings.Contains(r.text, "zakwas.yaml") {
		t.Errorf("without a config: error %v, %q", r.isErr, r.text)
	}
	if names := c.toolNames(); names["zakwas_plan"] == nil {
		t.Error("the server didn't start without a config")
	}
}

func TestMCPApplyReportsACommitThatFailedAfterTheApply(t *testing.T) {
	env, home := brewSetup(t)
	hook := filepath.Join(env.Cwd, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho hook says no >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	out := parseJSON(t, r.text)
	if !r.isErr || out["applied"] != true || out["committed"] != false {
		t.Errorf("outcome (error %v) = %v", r.isErr, out)
	}
	reason := out["reason"].(string)
	if !strings.HasPrefix(reason, "applied, but the commit failed") {
		t.Errorf("reason = %q", reason)
	}
	if _, err := os.Stat(HistoryPath(home)); err != nil {
		t.Errorf("the apply wasn't recorded: %v", err)
	}
}

func TestMCPCommitSaysWhereItWasApproved(t *testing.T) {
	env, _ := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	if r := c.tool("zakwas_apply", map[string]any{"id": id}, accept); r.isErr {
		t.Fatal(r.text)
	}
	if body := gitIn(t, env.Cwd, "log", "-1", "--format=%b"); !strings.Contains(body, "approved in the agent, by an MCP confirmation") {
		t.Errorf("commit body = %q", body)
	}
}

func TestReasonText(t *testing.T) {
	if got := reasonText("zakwas: one\nzakwas: two: three\n"); got != "one\ntwo: three" {
		t.Errorf("reasonText = %q", got)
	}
}

func TestMCPApplyStopsOnSIGTERMAndPutsTheFilesBack(t *testing.T) {
	env, home := brewSetup(t)
	installing := make(chan struct{})
	env.Runner = brewRunner{gitRunner: env.Runner.(brewRunner).gitRunner, install: func(ctx context.Context) {
		close(installing)
		<-ctx.Done()
	}}
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	brewfile := readFile(t, filepath.Join(env.Cwd, "Brewfile"))
	go func() {
		<-installing
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
		}
	}()

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	if out := parseJSON(t, r.text); out["applied"] == true {
		t.Errorf("outcome = %v", out)
	}
	if got := readFile(t, filepath.Join(env.Cwd, "Brewfile")); got != brewfile {
		t.Errorf("Brewfile wasn't put back:\n%s", got)
	}
	if gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("the repo is dirty")
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(home), id+".json")); err != nil {
		t.Errorf("the proposal is gone: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server ignored SIGTERM")
	}
}

func TestMCPApplyNeverAppliesAProposalDiscardedWhileItAsks(t *testing.T) {
	env, _ := brewSetup(t)
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	brewfile := readFile(t, filepath.Join(env.Cwd, "Brewfile"))

	r := c.tool("zakwas_apply", map[string]any{"id": id}, func(string) map[string]any {
		if err := os.Remove(filepath.Join(proposal.Dir(env.Home), id+".json")); err != nil {
			t.Error(err)
		}
		return accept("")
	})

	out := parseJSON(t, r.text)
	if out["applied"] == true || !strings.Contains(out["reason"].(string), "discarded") {
		t.Errorf("outcome = %v", out)
	}
	if readFile(t, filepath.Join(env.Cwd, "Brewfile")) != brewfile || gitIn(t, env.Cwd, "rev-list", "--count", "HEAD") != "1" {
		t.Error("the discarded proposal changed the repo")
	}
}

func TestMCPProposeRejectsInvisibleCharacters(t *testing.T) {
	env, _ := proposeSetup(t)
	c := startMCP(t, env, formClient)
	for name, command := range map[string]string{"bidi override": "ls \u202e txt.exe", "zero width space": "ls\u200b -la"} {
		r := c.tool("zakwas_propose", map[string]any{"kind": "add-permission", "decision": "deny", "command": command}, nil)
		if !r.isErr || !strings.Contains(r.text, "printable text") {
			t.Errorf("%s was accepted: %s", name, r.text)
		}
	}
	if got := plainText("a\u202eb\u200bc"); got != "abc" {
		t.Errorf("plainText = %q", got)
	}
}

func TestMCPStopsCleanlyOnSIGHUPAndSIGTERM(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			env, _ := setup(t, linksOnly)
			c := startMCP(t, env, formClient)
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-c.done:
			case <-time.After(10 * time.Second):
				t.Fatalf("the server ignored %v", sig)
			}
			if c.code != ExitOK {
				t.Errorf("exit %d, want %d", c.code, ExitOK)
			}
		})
	}
}
