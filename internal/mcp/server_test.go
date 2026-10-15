package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type client struct {
	t     *testing.T
	in    io.WriteCloser
	out   *bufio.Reader
	errCh chan error
}

func start(t *testing.T, tools ...Tool) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &Server{Name: "test", Version: "1", Instructions: "be nice", Tools: tools, In: inR, Out: outW}
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), errCh: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c.errCh <- s.Serve(ctx)
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
	})
	return c
}

func (c *client) send(v any) {
	c.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() map[string]any {
	c.t.Helper()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.out.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("read: %v", r.err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.line), &m); err != nil {
			c.t.Fatalf("bad line %q: %v", r.line, err)
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for the server")
		return nil
	}
}

func (c *client) call(id int, method string, params any) map[string]any {
	c.t.Helper()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return c.read()
}

func (c *client) initialize(version string, caps map[string]any) map[string]any {
	c.t.Helper()
	got := c.call(1, "initialize", map[string]any{"protocolVersion": version, "capabilities": caps, "clientInfo": map[string]any{"name": "t"}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return got["result"].(map[string]any)
}

func echo() Tool {
	return Tool{
		Name: "echo", Title: "Echo", Description: "Echoes", ReadOnly: true,
		Handler: func(_ context.Context, c *Call) (Result, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := c.Decode(&args); err != nil {
				return Result{}, err
			}
			return Result{Text: args.Text}, nil
		},
	}
}

func text(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestInitializeNegotiatesTheVersion(t *testing.T) {
	tests := []struct {
		name, asked, want string
	}{
		{"newest", "2025-06-18", "2025-06-18"},
		{"older", "2024-11-05", "2024-11-05"},
		{"future", "2030-01-01", "2025-06-18"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := start(t, echo())
			got := c.initialize(tc.asked, map[string]any{})
			if got["protocolVersion"] != tc.want {
				t.Errorf("version %v, want %s", got["protocolVersion"], tc.want)
			}
			if got["instructions"] != "be nice" {
				t.Errorf("instructions %v", got["instructions"])
			}
			info := got["serverInfo"].(map[string]any)
			if info["name"] != "test" {
				t.Errorf("serverInfo %v", info)
			}
		})
	}
}

func TestToolsListMarksInteractiveAndReadOnlyTools(t *testing.T) {
	apply := Tool{
		Name: "apply", Title: "Apply", Description: "d", Destructive: true, Interactive: true,
		Handler: func(context.Context, *Call) (Result, error) { return Result{}, nil },
	}
	c := start(t, echo(), apply)
	c.initialize("2025-06-18", map[string]any{})

	got := c.call(2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(got) != 2 {
		t.Fatalf("got %d tools", len(got))
	}
	echoTool, applyTool := got[0].(map[string]any), got[1].(map[string]any)
	if _, has := echoTool["_meta"]; has {
		t.Errorf("echo is marked interactive: %v", echoTool)
	}
	if echoTool["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Errorf("echo is not read-only: %v", echoTool)
	}
	meta := applyTool["_meta"].(map[string]any)
	if meta["anthropic/requiresUserInteraction"] != true {
		t.Errorf("apply _meta %v", meta)
	}
	ann := applyTool["annotations"].(map[string]any)
	if ann["readOnlyHint"] != false || ann["destructiveHint"] != true {
		t.Errorf("apply annotations %v", ann)
	}
	if applyTool["inputSchema"].(map[string]any)["type"] != "object" {
		t.Errorf("apply has no object schema: %v", applyTool)
	}
}

func TestToolCall(t *testing.T) {
	c := start(t, echo())
	c.initialize("2025-06-18", map[string]any{})

	got, isErr := text(t, c.call(2, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "hi"}}))
	if got != "hi" || isErr {
		t.Errorf("got %q (error %v)", got, isErr)
	}

	got, isErr = text(t, c.call(3, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"nope": 1}}))
	if !isErr || !strings.Contains(got, "bad arguments") {
		t.Errorf("unknown argument gave %q (error %v)", got, isErr)
	}

	resp := c.call(4, "tools/call", map[string]any{"name": "missing"})
	if e, ok := resp["error"].(map[string]any); !ok || e["code"].(float64) != codeInvalidParams {
		t.Errorf("unknown tool gave %v", resp)
	}
	resp = c.call(5, "nope/nope", nil)
	if e, ok := resp["error"].(map[string]any); !ok || e["code"].(float64) != codeMethodNotFound {
		t.Errorf("unknown method gave %v", resp)
	}
	if resp := c.call(6, "ping", nil); resp["error"] != nil {
		t.Errorf("ping failed: %v", resp)
	}
}

func TestHandlerPanicIsAToolError(t *testing.T) {
	boom := Tool{Name: "boom", Handler: func(context.Context, *Call) (Result, error) { panic("kaput") }}
	c := start(t, boom, echo())
	c.initialize("2025-06-18", map[string]any{})

	got, isErr := text(t, c.call(2, "tools/call", map[string]any{"name": "boom"}))
	if !isErr || !strings.Contains(got, "kaput") {
		t.Errorf("got %q (error %v)", got, isErr)
	}
	if got, _ := text(t, c.call(3, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "still up"}})); got != "still up" {
		t.Errorf("server stopped answering: %q", got)
	}
}

func TestProgressNeedsAToken(t *testing.T) {
	work := Tool{Name: "work", Handler: func(_ context.Context, c *Call) (Result, error) {
		c.Progress(1, 2, "half")
		c.Progress(2, 2, "done")
		return Result{Text: "ok"}, nil
	}}
	c := start(t, work)
	c.initialize("2025-06-18", map[string]any{})

	c.send(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": "work", "_meta": map[string]any{"progressToken": "tok"}},
	})
	for i, want := range []string{"half", "done"} {
		n := c.read()
		p := n["params"].(map[string]any)
		if n["method"] != "notifications/progress" || p["progressToken"] != "tok" || p["message"] != want || p["progress"].(float64) != float64(i+1) {
			t.Errorf("notification %d: %v", i, n)
		}
	}
	if got, _ := text(t, c.read()); got != "ok" {
		t.Errorf("result %q", got)
	}

	if got, _ := text(t, c.call(3, "tools/call", map[string]any{"name": "work"})); got != "ok" {
		t.Errorf("without a token the first line should be the result, got %q", got)
	}
}

func askTool(answers chan<- ElicitResponse, errs chan<- error) Tool {
	return Tool{Name: "ask", Handler: func(ctx context.Context, c *Call) (Result, error) {
		resp, err := c.Elicit(ctx, "Proceed?", map[string]any{"ok": map[string]any{"type": "boolean"}}, "ok")
		if err != nil {
			errs <- err
			return Result{}, err
		}
		answers <- resp
		return Result{Text: resp.Action}, nil
	}}
}

func TestElicitation(t *testing.T) {
	answers, errs := make(chan ElicitResponse, 1), make(chan error, 1)
	c := start(t, askTool(answers, errs))
	c.initialize("2025-06-18", map[string]any{"elicitation": map[string]any{}})

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "ask"}})
	req := c.read()
	if req["method"] != "elicitation/create" {
		t.Fatalf("got %v, want an elicitation request", req)
	}
	params := req["params"].(map[string]any)
	if params["message"] != "Proceed?" {
		t.Errorf("message %v", params["message"])
	}
	schema := params["requestedSchema"].(map[string]any)
	if schema["type"] != "object" || schema["required"].([]any)[0] != "ok" {
		t.Errorf("schema %v", schema)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"ok": true}}})

	if got, _ := text(t, c.read()); got != "accept" {
		t.Errorf("result %q", got)
	}
	if a := <-answers; a.Content["ok"] != true {
		t.Errorf("answer %+v", a)
	}
}

func TestElicitationNeedsAClientThatCanShowForms(t *testing.T) {
	tests := []struct {
		name    string
		version string
		caps    map[string]any
	}{
		{"no capability", "2025-06-18", map[string]any{}},
		{"url mode only", "2025-06-18", map[string]any{"elicitation": map[string]any{"url": map[string]any{}}}},
		{"protocol too old", "2025-03-26", map[string]any{"elicitation": map[string]any{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			answers, errs := make(chan ElicitResponse, 1), make(chan error, 1)
			c := start(t, askTool(answers, errs))
			c.initialize(tc.version, tc.caps)

			got, isErr := text(t, c.call(2, "tools/call", map[string]any{"name": "ask"}))
			if !isErr || !strings.Contains(got, "can't ask the user") {
				t.Errorf("got %q (error %v)", got, isErr)
			}
		})
	}
}

func TestElicitationErrorsEndTheCall(t *testing.T) {
	answers, errs := make(chan ElicitResponse, 1), make(chan error, 1)
	c := start(t, askTool(answers, errs))
	c.initialize("2025-06-18", map[string]any{"elicitation": map[string]any{"form": map[string]any{}}})

	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "ask"}})
	req := c.read()
	c.send(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": -32601, "message": "not supported"}})

	got, isErr := text(t, c.read())
	if !isErr || !strings.Contains(got, "not supported") {
		t.Errorf("got %q (error %v)", got, isErr)
	}
}

func TestCancelledCallGetsNoAnswer(t *testing.T) {
	started := make(chan struct{})
	slow := Tool{Name: "slow", Handler: func(ctx context.Context, _ *Call) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, errors.New("cancelled")
	}}
	c := start(t, slow, echo())
	c.initialize("2025-06-18", map[string]any{})

	c.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "slow"}})
	<-started
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 7}})

	if got, _ := text(t, c.call(8, "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{"text": "next"}})); got != "next" {
		t.Errorf("the next answer should be echo's, got %q", got)
	}
}

func TestServeStopsWhenInputCloses(t *testing.T) {
	c := start(t, echo())
	c.initialize("2025-06-18", map[string]any{})
	_ = c.in.Close()
	select {
	case err := <-c.errCh:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestClosingInputCancelsCallsInFlight(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	started := make(chan struct{})
	stuck := Tool{Name: "stuck", Handler: func(ctx context.Context, _ *Call) (Result, error) {
		defer wg.Done()
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}
	c := start(t, stuck)
	c.initialize("2025-06-18", map[string]any{})
	c.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "stuck"}})
	<-started
	_ = c.in.Close()
	go func() { _, _ = io.Copy(io.Discard, c.out) }()

	wg.Wait()
	select {
	case <-c.errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestGarbageGetsAnError(t *testing.T) {
	c := start(t, echo())
	if _, err := io.WriteString(c.in, "not json\n"); err != nil {
		t.Fatal(err)
	}
	resp := c.read()
	if e, ok := resp["error"].(map[string]any); !ok || e["code"].(float64) != codeInvalidRequest {
		t.Errorf("got %v", resp)
	}
}
