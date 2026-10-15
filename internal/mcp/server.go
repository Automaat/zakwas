// Package mcp is a small Model Context Protocol server over stdio: JSON-RPC
// 2.0 messages, one per line, serving tools, progress notifications and
// elicitation requests to the user.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
)

// protocolVersions are the revisions the server speaks, newest first.
// Elicitation exists from 2025-06-18 on.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const elicitationSince = "2025-06-18"

const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Tool is one tool the server offers. Schema is the JSON Schema of its
// arguments, an object. ReadOnly marks a tool that changes nothing and
// Destructive one that changes or removes state. Interactive marks a tool
// a client must ask the user about on every call, whatever its permission
// mode: Claude Code's anthropic/requiresUserInteraction.
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	Destructive bool
	Interactive bool
	Handler     func(ctx context.Context, c *Call) (Result, error)
}

// Result is what a tool call returns to the model.
type Result struct {
	Text    string
	IsError bool
}

// Server serves Tools over In and Out until In closes. The negotiated
// whether the client can show a form is learned from its initialize request; pending holds the requests the server sent and
// waits an answer to, and inflight the client's requests being handled.
type Server struct {
	Name, Version string
	Instructions  string
	Tools         []Tool
	In            io.Reader
	Out           io.Writer

	writeMu  sync.Mutex
	nextID   atomic.Int64
	pending  sync.Map
	inflight sync.Map
	elicits  atomic.Bool
	done     chan struct{}
}

type request struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Serve reads requests until the input closes or ctx ends. When the input
// closes, calls in flight finish, since stopping one halfway is what leaves
// a half-applied change; only a form waiting for the user's answer fails,
// as nobody is left to answer it. When ctx ends, they are cancelled.
func (s *Server) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.done = make(chan struct{})
	var closeOnce sync.Once
	closeInput := func() { closeOnce.Do(func() { close(s.done) }) }
	defer closeInput()

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		r := bufio.NewReader(s.In)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr <- err
				}
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				closeInput()
				wg.Wait()
				select {
				case err := <-readErr:
					return err
				default:
					return nil
				}
			}
			s.dispatch(ctx, &wg, line)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, wg *sync.WaitGroup, line []byte) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil || m.JSONRPC != "2.0" {
		s.reply(nil, nil, &rpcError{codeInvalidRequest, "not a JSON-RPC 2.0 message"})
		return
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method == "" && hasID:
		if ch, ok := s.pending.Load(string(m.ID)); ok {
			select {
			case ch.(chan message) <- m:
			default:
			}
		}
	case m.Method != "" && !hasID:
		s.notification(m)
	case m.Method != "":
		req := &request{}
		var rctx context.Context
		rctx, req.cancel = context.WithCancel(ctx)
		s.inflight.Store(string(m.ID), req)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer req.cancel()
			defer s.inflight.Delete(string(m.ID))
			result, rerr := s.handle(rctx, m)
			if req.cancelled.Load() {
				return
			}
			s.reply(m.ID, result, rerr)
		}()
	default:
		s.reply(nil, nil, &rpcError{codeInvalidRequest, "message has neither a method nor an id"})
	}
}

func (s *Server) notification(m message) {
	if m.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	if v, ok := s.inflight.Load(string(p.RequestID)); ok {
		req := v.(*request)
		req.cancelled.Store(true)
		req.cancel()
	}
}

func (s *Server) handle(ctx context.Context, m message) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		return s.initialize(m.Params)
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": s.toolList()}, nil
	case "tools/call":
		return s.call(ctx, m)
	}
	return nil, &rpcError{codeMethodNotFound, "unknown method " + m.Method}
}

func (s *Server) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Elicitation map[string]json.RawMessage `json:"elicitation"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "bad initialize params: " + err.Error()}
	}
	version := protocolVersions[0]
	if slices.Contains(protocolVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	_, form := p.Capabilities.Elicitation["form"]
	formsOnly := len(p.Capabilities.Elicitation) == 0
	s.elicits.Store(p.Capabilities.Elicitation != nil && (formsOnly || form) && version >= elicitationSince)
	out := map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
	}
	if s.Instructions != "" {
		out["instructions"] = s.Instructions
	}
	return out, nil
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.Tools))
	for _, t := range s.Tools {
		schema := t.Schema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		entry := map[string]any{
			"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": schema,
			"annotations": map[string]any{
				"readOnlyHint": t.ReadOnly, "destructiveHint": !t.ReadOnly && t.Destructive,
				"idempotentHint": t.ReadOnly, "openWorldHint": false,
			},
		}
		if t.Interactive {
			entry["_meta"] = map[string]any{"anthropic/requiresUserInteraction": true}
		}
		out = append(out, entry)
	}
	return out
}

func (s *Server) call(ctx context.Context, m message) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "bad tools/call params: " + err.Error()}
	}
	i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
	if i < 0 {
		return nil, &rpcError{codeInvalidParams, "unknown tool " + strconv.Quote(p.Name)}
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	call := &Call{Args: p.Arguments, s: s, token: p.Meta.ProgressToken}
	res, err := s.run(ctx, s.Tools[i], call)
	if err != nil {
		res = Result{Text: err.Error(), IsError: true}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": res.Text}},
		"isError": res.IsError,
	}, nil
}

// run calls the handler, turning a panic into a tool error so one bad call
// doesn't end the session.
func (s *Server) run(ctx context.Context, t Tool, c *Call) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s failed: %v", t.Name, r)
		}
	}()
	return t.Handler(ctx, c)
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	m := map[string]any{"jsonrpc": "2.0", "id": nil}
	if len(id) > 0 {
		m["id"] = id
	}
	if rerr != nil {
		m["error"] = rerr
	} else {
		m["result"] = result
	}
	_ = s.send(m)
}

func (s *Server) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.Out.Write(append(data, '\n'))
	return err
}

// Call is one tool call in progress.
type Call struct {
	Args  json.RawMessage
	s     *Server
	token json.RawMessage
}

// Decode reads the arguments into v, refusing fields v doesn't have.
func (c *Call) Decode(v any) error {
	dec := json.NewDecoder(bytes.NewReader(c.Args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// Progress tells the client how far the call is, when it asked to be told.
// progress must grow with every call.
func (c *Call) Progress(progress, total float64, text string) {
	if len(c.token) == 0 || string(c.token) == "null" {
		return
	}
	params := map[string]any{"progressToken": c.token, "progress": progress}
	if total > 0 {
		params["total"] = total
	}
	if text != "" {
		params["message"] = text
	}
	_ = c.s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": params})
}

// CanElicit reports whether the client said it can show the user a form.
func (c *Call) CanElicit() bool { return c.s.elicits.Load() }

// Elicitation actions.
const (
	ActionAccept  = "accept"
	ActionDecline = "decline"
	ActionCancel  = "cancel"
)

// ElicitResponse is the user's answer to a form.
type ElicitResponse struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content"`
}

// Elicit shows the user text and a form with the given properties and
// waits for the answer. It fails when the client can't show forms, the
// answer doesn't come before ctx ends, or the server stops.
func (c *Call) Elicit(ctx context.Context, text string, properties map[string]any, required ...string) (ElicitResponse, error) {
	var out ElicitResponse
	if !c.CanElicit() {
		return out, errors.New("the client can't ask the user")
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	id := strconv.FormatInt(c.s.nextID.Add(1), 10)
	ch := make(chan message, 1)
	c.s.pending.Store(id, ch)
	defer c.s.pending.Delete(id)
	if err := c.s.send(map[string]any{
		"jsonrpc": "2.0", "id": json.Number(id), "method": "elicitation/create",
		"params": map[string]any{"message": text, "requestedSchema": schema},
	}); err != nil {
		return out, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return out, m.Error
		}
		if err := json.Unmarshal(m.Result, &out); err != nil {
			return out, fmt.Errorf("bad elicitation answer: %w", err)
		}
		return out, nil
	case <-ctx.Done():
		return out, ctx.Err()
	case <-c.s.done:
		return out, errors.New("the client closed the connection")
	}
}
