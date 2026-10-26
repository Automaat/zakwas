package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/mcp"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner"
)

const mcpUsage = `Usage: zakwas mcp [-c zakwas.yaml] [--enable-apply]

Serves zakwas to a coding agent over MCP on stdin and stdout. The tools read
the config (zakwas_config), plan and check it (zakwas_plan, zakwas_check),
scan the agents for what the config doesn't declare (zakwas_capture), and
save, list and drop proposals (zakwas_propose, zakwas_proposals,
zakwas_discard). None of them changes the config repo or the system.

With --enable-apply the server also offers zakwas_apply, which applies a low
risk proposal after the user confirms the diff and plan zakwas shows in the
agent. High risk proposals, and clients that can't show the confirmation,
are sent to 'zakwas approve ID' in a terminal.

Register it with the agents through agents.mcp, giving -c or ZAKWAS_CONFIG
since an agent starts it in any directory.

Flags:
`

func mcpInstructions(goos string) string {
	return strings.ReplaceAll(mcpInstructionsText, "this Mac", "this "+platform.Machine(goos))
}

const mcpInstructionsText = `zakwas keeps this Mac's setup in a config repo. Read it with zakwas_config, zakwas_plan and zakwas_check.
To change the setup, never edit agent config files or zakwas.yaml: call zakwas_propose, which saves a proposal and changes nothing.
To record what was added in an agent by hand, call zakwas_capture and pass the operations it returns to zakwas_propose.
Then tell the user the proposal's id and tier. A proposal applies only after the user confirms it; when zakwas_apply isn't offered, or for a high risk proposal, give the user the command in the proposal's "next" field to run in a terminal.
Drop a proposal that is no longer wanted with zakwas_discard.`

type mcpOptions struct {
	cfgPath     string
	enableApply bool
}

func newMCPFlags(errOut io.Writer) (*flag.FlagSet, *mcpOptions) {
	var o mcpOptions
	fs := flag.NewFlagSet("zakwas mcp", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), mcpUsage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	fs.BoolVar(&o.enableApply, "enable-apply", false, "also offer zakwas_apply, which applies a low risk proposal after the user confirms it in the agent")
	return fs, &o
}

const (
	confirmTimeout = 10 * time.Minute
	maxConfirmText = 32 << 10
	maxConfigYAML  = 256 << 10
)

func runMCP(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, o := newMCPFlags(errOut)
	pos, code, ok := parseSub(fs, out, args)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fs.Usage()
		return ExitUsage
	}
	if ex, ok := env.Runner.(*runner.Exec); ok {
		ex.Stdout, ex.NoStdin = ex.Stderr, true
		ex.Env = append(ex.Env, LockHeldEnv+"="+strconv.Itoa(os.Getpid()))
	}
	signal.Ignore(syscall.SIGPIPE)
	defer signal.Reset(syscall.SIGPIPE)
	stopOn := []os.Signal{syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		stopOn = append(stopOn, syscall.SIGHUP)
	}
	ctx, stop := signal.NotifyContext(ctx, stopOn...)
	defer stop()
	t := &mcpTools{env: env, cfgPath: o.cfgPath, enableApply: o.enableApply}
	srv := &mcp.Server{
		Name: "zakwas", Version: env.Version, Instructions: mcpInstructions(env.goos()),
		Tools: t.tools(), In: env.Stdin, Out: env.Stdout,
	}
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		errOut.fail(err)
		return ExitErr
	}
	return ExitOK
}

type mcpTools struct {
	env         Env
	cfgPath     string
	enableApply bool
}

func (t *mcpTools) load() (*config.Config, error) {
	return loadConfig(t.cfgPath, t.env)
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func stringsProp(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}

func (t *mcpTools) tools() []mcp.Tool {
	planProps := map[string]any{
		"only": stringsProp("Modules to plan, e.g. [\"agents\"]; default all of them."),
		"diff": map[string]any{"type": "boolean", "description": "Include file diffs and command scripts."},
	}
	idSchema := objectSchema(map[string]any{"id": stringProp("Proposal id, or a prefix that names only one.")}, "id")
	tools := []mcp.Tool{
		{
			Name: "zakwas_config", Title: "Read the zakwas config", ReadOnly: true,
			Description: "Where the zakwas config is, which modules and agent providers it configures, whether the guard is on, and the text of zakwas.yaml.",
			Handler:     t.config,
		},
		{
			Name: "zakwas_plan", Title: "Plan the config", ReadOnly: true, Schema: objectSchema(planProps),
			Description: "What `zakwas apply` would change on " + "this " + platform.Machine(t.env.goos()) + ", as JSON. Changes nothing.",
			Handler:     func(ctx context.Context, c *mcp.Call) (mcp.Result, error) { return t.plan(ctx, c, false) },
		},
		{
			Name: "zakwas_check", Title: "Check for drift", ReadOnly: true, Schema: objectSchema(planProps),
			Description: "Whether this " + platform.Machine(t.env.goos()) + " drifted from the config: {\"drift\": bool} with the plan. Changes nothing.",
			Handler:     func(ctx context.Context, c *mcp.Call) (mcp.Result, error) { return t.plan(ctx, c, true) },
		},
		{
			Name: "zakwas_capture", Title: "Scan for undeclared agent changes", ReadOnly: true,
			Description: "What the managed agents have that zakwas.yaml doesn't declare: plugins, marketplaces and MCP servers added in the agents, Claude allow rules and Codex approvals. " +
				"Returns the findings, the ones that can't be declared with the reason, and in \"ops\" the proposal operations that declare the rest, each valid for zakwas_propose; pass them together as {\"ops\": [...]}. Changes nothing.",
			Handler: t.capture,
		},
		{
			Name: "zakwas_propose", Title: "Propose a config change", Schema: proposalRequestSchema(t.env.goos()),
			Description: "Saves a change to the config repo as a proposal, with its diff, plan and risk tier, and changes nothing else. Pass one operation or {\"ops\": [operation, ...]} for one combined proposal; set layer to send it to a registered local layer's repo. Operations: " +
				strings.Join(proposal.KindsFor(t.env.goos()), ", ") + ". Returns the proposal and what to do next.",
			Handler: t.propose,
		},
		{
			Name: "zakwas_proposals", Title: "List proposals", ReadOnly: true,
			Description: "The pending proposals of this config: id, tier, summary and when each expires.",
			Handler:     t.proposals,
		},
		{
			Name: "zakwas_discard", Title: "Discard a proposal", Destructive: true, Schema: idSchema,
			Description: "Deletes a pending proposal of this config. Changes nothing else.",
			Handler:     t.discard,
		},
	}
	if t.enableApply {
		tools = append(tools, mcp.Tool{
			Name: config.MCPApplyTool, Title: "Apply a proposal", Destructive: true, Interactive: true, Schema: idSchema,
			Description: "Applies a low risk proposal and commits the edited config files without pushing, after the user confirms the diff and plan in the agent. " +
				"Refuses a high risk proposal, or a client that can't show the confirmation, and returns the `zakwas approve` command to run in a terminal instead.",
			Handler: t.apply,
		})
	}
	return tools
}

func proposalRequestSchema(goos string) map[string]any {
	single := proposeSchema(goos)
	props := maps.Clone(single["properties"].(map[string]any))
	props["ops"] = map[string]any{
		"type": "array", "minItems": 1, "items": single,
		"description": "Ordered operations saved and approved as one proposal, all in one repo. Do not combine with single-operation fields except layer, which applies to the operations that name none.",
	}
	schema := objectSchema(props)
	schema["oneOf"] = []any{map[string]any{"required": []string{"kind"}}, map[string]any{"required": []string{"ops"}}}
	return schema
}

func proposeSchema(goos string) map[string]any {
	server := objectSchema(map[string]any{
		"command":          stringProp("Command that starts a stdio server."),
		"args":             stringsProp("Arguments of the command."),
		"url":              stringProp("URL of an HTTP server."),
		"env_vars":         stringsProp("Environment variables passed through to the command."),
		"bearer_token_env": stringProp("Environment variable holding the bearer token of a url server."),
		"oauth": objectSchema(map[string]any{
			"client_id":     stringProp("Pre-registered OAuth client ID of a url server. Public; a client secret is not supported."),
			"callback_port": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535, "description": "Fixed port of the local OAuth callback."},
			"scope":         stringProp("OAuth scopes separated by single spaces."),
		}, "client_id"),
	})
	props := map[string]any{
		"kind":          map[string]any{"type": "string", "enum": proposal.KindsFor(goos), "description": "The operation."},
		"name":          stringProp("add-marketplace: marketplace name. add-plugin: NAME@MARKETPLACE. add-brew: package. pin-mise: tool. add-mcp: server name."),
		"source":        stringProp("add-marketplace: GitHub owner/repo, a git URL, or a local path."),
		"decision":      map[string]any{"type": "string", "enum": []string{config.PermissionAllow, config.PermissionReview, config.PermissionDeny}, "description": "add-permission and remove-permission: the rule list."},
		"command":       stringProp("add-permission and remove-permission: the command prefix, e.g. \"git status\"."),
		"justification": stringProp("add-permission: one-line reason Codex shows for a prompting or forbidden command."),
		"version":       stringProp("pin-mise: the version."),
		"cask":          map[string]any{"type": "boolean", "description": "add-brew: the package is a cask."},
		"providers":     stringsProp("Providers the entry targets; default the section's."),
		"layer":         stringProp("Name of a registered local layer (zakwas_config lists them): the change goes to that layer's zakwas.yaml, Brewfile or mise config and is committed in its repo. Default: the main config. An edit or removal goes to the file that holds the entry; naming another layer is an error."),
		"server":        server,
	}
	if !platform.Supported("brew", goos) {
		delete(props, "cask")
	}
	if !platform.Supported("mise", goos) {
		delete(props, "version")
	}
	if !platform.Supported("brew", goos) && !platform.Supported("mise", goos) {
		props["name"] = stringProp("add-marketplace: marketplace name. add-plugin: NAME@MARKETPLACE. add-mcp: server name.")
	}
	return objectSchema(props, "kind")
}

func jsonResult(v any) (mcp.Result, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Text: string(data)}, nil
}

// layerInfo is a registered layer as zakwas_config shows it: where it is
// and which commit its checkout is on.
type layerInfo struct {
	config.Layer
	Commit string `json:"commit,omitempty"`
}

func (t *mcpTools) config(ctx context.Context, _ *mcp.Call) (mcp.Result, error) {
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	var names []string
	for _, m := range Modules(cfg, t.env) {
		names = append(names, m.Name())
	}
	out := struct {
		Config    string      `json:"config"`
		Root      string      `json:"root"`
		Modules   []string    `json:"modules"`
		Providers []string    `json:"agent_providers,omitempty"`
		MCP       []string    `json:"agent_mcp_servers,omitempty"`
		Commit    string      `json:"commit,omitempty"`
		Layers    []layerInfo `json:"layers,omitempty"`
		Guard     bool        `json:"guard"`
		Apply     bool        `json:"apply_enabled"`
		YAML      string      `json:"yaml"`
	}{Config: cfg.Path, Root: cfg.Root, Modules: names, Guard: cfg.Agents.GuardEnabled(), Apply: t.enableApply}
	out.Commit, _ = git(ctx, t.env.Runner, cfg.Root, "rev-parse", "HEAD")
	for i, lc := range layerCommitsOf(ctx, t.env.Runner, cfg.Layers, false) {
		out.Layers = append(out.Layers, layerInfo{Layer: cfg.Layers[i].Layer, Commit: lc.Commit})
	}
	if cfg.Agents != nil {
		out.Providers = cfg.Agents.DefaultProviders()
		out.MCP = slices.Sorted(maps.Keys(cfg.Agents.MCP))
	}
	data, err := os.ReadFile(cfg.Path)
	switch {
	case err != nil:
		return mcp.Result{}, err
	case len(data) > maxConfigYAML:
		out.YAML = fmt.Sprintf("(%d bytes, too long to include; read %s)", len(data), cfg.Path)
	default:
		out.YAML = string(data)
	}
	return jsonResult(out)
}

func (t *mcpTools) plan(ctx context.Context, c *mcp.Call, check bool) (mcp.Result, error) {
	var args struct {
		Only []string `json:"only"`
		Diff bool     `json:"diff"`
	}
	if err := c.Decode(&args); err != nil {
		return mcp.Result{}, err
	}
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	only := strings.Join(args.Only, ",")
	mods, err := selectModules(Modules(cfg, t.env), only, t.env.goos())
	if err != nil {
		return mcp.Result{}, err
	}
	plan := engine.Build(ctx, mods)
	commit, _ := git(ctx, t.env.Runner, cfg.Root, "rev-parse", "HEAD")
	doc := newPlanDoc(t.env, cfg.Path, commit, layerCommitsOf(ctx, t.env.Runner, cfg.Layers, false), args.Only, plan, args.Diff)
	var res mcp.Result
	if check {
		res, err = jsonResult(struct {
			Drift bool `json:"drift"`
			planDoc
		}{!plan.Empty(), doc})
	} else {
		res, err = jsonResult(doc)
	}
	if err != nil {
		return res, err
	}
	if planErr := plan.Err(); planErr != nil {
		res.IsError = true
		res.Text += "\n" + planErr.Error()
	}
	return res, nil
}

func (t *mcpTools) capture(ctx context.Context, _ *mcp.Call) (mcp.Result, error) {
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	doc, scanErr := scanCapture(ctx, t.env, cfg)
	if errors.Is(scanErr, errNoAgents) {
		return mcp.Result{}, scanErr
	}
	res, err := jsonResult(doc)
	if err == nil && scanErr != nil {
		res.IsError = true
		res.Text += "\n" + scanErr.Error()
	}
	return res, err
}

func (t *mcpTools) next(p *proposal.Proposal) string {
	approve := "zakwas approve " + p.ID
	switch {
	case p.Tier == proposal.TierLow && t.enableApply:
		return fmt.Sprintf("Low risk. Call %s with this id: the user is asked to confirm the diff and plan before anything changes. Or the user can run `%s` in a terminal.", config.MCPApplyTool, approve)
	case p.Tier == proposal.TierLow:
		return fmt.Sprintf("Low risk, but this server doesn't apply proposals. Ask the user to run `%s` in a terminal.", approve)
	}
	return fmt.Sprintf("High risk: only a terminal applies it, never an agent. Give the user this command to run: `%s`", approve)
}

func (t *mcpTools) propose(ctx context.Context, c *mcp.Call) (mcp.Result, error) {
	var fields map[string]json.RawMessage
	if err := c.Decode(&fields); err != nil {
		return mcp.Result{}, err
	}
	var ops []proposal.Op
	if data, batch := fields["ops"]; batch {
		var layer string
		allowed := 1
		if raw, ok := fields["layer"]; ok {
			allowed++
			if err := json.Unmarshal(raw, &layer); err != nil {
				return mcp.Result{}, fmt.Errorf("layer must be a string: %w", err)
			}
		}
		if len(fields) != allowed {
			return mcp.Result{}, errors.New("ops cannot be combined with single-operation fields other than layer")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ops); err != nil {
			return mcp.Result{}, fmt.Errorf("ops must be an array of operations: %w", err)
		}
		if err := proposal.DefaultLayer(ops, layer); err != nil {
			return mcp.Result{}, err
		}
	} else {
		var op proposal.Op
		if err := c.Decode(&op); err != nil {
			return mcp.Result{}, err
		}
		ops = []proposal.Op{op}
	}
	if err := proposal.ValidateOps(ops); err != nil {
		return mcp.Result{}, err
	}
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	p, _, err := saveProposal(ctx, t.env, cfg, ops...)
	if err != nil {
		return mcp.Result{}, err
	}
	return jsonResult(struct {
		*proposal.Proposal
		Next string `json:"next"`
	}{p, t.next(p)})
}

func (t *mcpTools) proposals(context.Context, *mcp.Call) (mcp.Result, error) {
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	all, err := (proposal.Store{Dir: proposal.Dir(t.env.Home)}).List()
	if err != nil {
		return mcp.Result{}, err
	}
	type entry struct {
		ID      string    `json:"id"`
		Tier    string    `json:"tier"`
		Summary string    `json:"summary"`
		Layer   string    `json:"layer,omitempty"`
		Expires time.Time `json:"expires"`
		Expired bool      `json:"expired"`
	}
	out := []entry{}
	now := t.env.now()
	for _, p := range all {
		if p.Config == cfg.Path {
			out = append(out, entry{p.ID, printable(p.Tier), printable(p.Summary), printable(p.Layer), p.Expires(), p.Expired(now)})
		}
	}
	return jsonResult(struct {
		FormatVersion string  `json:"format_version"`
		Proposals     []entry `json:"proposals"`
	}{engine.FormatVersion, out})
}

type idArgs struct {
	ID string `json:"id"`
}

func (t *mcpTools) discard(_ context.Context, c *mcp.Call) (mcp.Result, error) {
	var args idArgs
	if err := c.Decode(&args); err != nil {
		return mcp.Result{}, err
	}
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	store := proposal.Store{Dir: proposal.Dir(t.env.Home)}
	p, err := store.Load(args.ID)
	if err != nil {
		return mcp.Result{}, err
	}
	if p.Config != cfg.Path {
		return mcp.Result{}, fmt.Errorf("proposal %s was made for %s, not %s", p.ID, p.Config, cfg.Path)
	}
	if err := store.Delete(p.ID); err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Text: "discarded proposal " + p.ID + ": " + printable(p.Summary)}, nil
}

type applyOutcome struct {
	Applied   bool   `json:"applied"`
	Committed bool   `json:"committed"`
	Reason    string `json:"reason,omitempty"`
	Command   string `json:"command,omitempty"`
	Output    string `json:"output,omitempty"`
}

func (t *mcpTools) apply(ctx context.Context, c *mcp.Call) (mcp.Result, error) {
	var args idArgs
	if err := c.Decode(&args); err != nil {
		return mcp.Result{}, err
	}
	if !c.CanElicit() {
		return jsonResult(applyOutcome{
			Reason:  "this client can't ask the user to confirm, so nothing was applied",
			Command: "zakwas approve " + args.ID,
		})
	}
	cfg, err := t.load()
	if err != nil {
		return mcp.Result{}, err
	}
	store := proposal.Store{Dir: proposal.Dir(t.env.Home)}
	p, err := store.Load(args.ID)
	if err != nil {
		return mcp.Result{}, err
	}
	if err := checkProposal(p, cfg, t.env); err != nil {
		return mcp.Result{}, err
	}
	var errBuf, logBuf, lockBuf bytes.Buffer
	held, ok := takeLock(ctx, t.env, "mcp apply", true, &console{w: &lockBuf})
	if !ok {
		return mcp.Result{}, errors.New(reasonText(lockBuf.String()))
	}
	defer func() { _ = held.Release() }()

	env := t.env
	env.Color, env.GitHubActions = false, false
	ap := &approval{env: env, cfg: cfg, p: p, out: &console{w: &logBuf}, errOut: &console{w: &errBuf}, maxTier: proposal.TierLow, via: "the agent, by an MCP confirmation"}
	ap.ask = t.confirm(c, ap)
	ap.observer = func(style engine.Style) engine.Observer {
		return observers{&progress{out: ap.out, style: style}, mcpProgress{c}}
	}
	code := ap.run(ctx, store)

	problems := reasonText(errBuf.String())
	switch {
	case code == ExitOK:
		return jsonResult(applyOutcome{Applied: true, Committed: true, Output: strings.TrimSpace(logBuf.String())})
	case ap.applied:
		res, err := jsonResult(applyOutcome{Applied: true, Reason: problems, Output: strings.TrimSpace(logBuf.String())})
		res.IsError = true
		return res, err
	case ap.outcome != "":
		reason := problems
		if reason == "" {
			reason = "the user declined"
		}
		return jsonResult(applyOutcome{Reason: reason, Command: "zakwas approve " + p.ID})
	}
	res, err := jsonResult(applyOutcome{Reason: problems, Output: strings.TrimSpace(logBuf.String())})
	res.IsError = true
	return res, err
}

// reasonText is what zakwas printed to its error output, without the
// "zakwas: " that starts each line there.
func reasonText(s string) string {
	var lines []string
	for line := range strings.Lines(s) {
		lines = append(lines, strings.TrimPrefix(strings.TrimRight(line, "\n"), "zakwas: "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// confirm asks the user in the agent to approve the review zakwas renders.
// Anything but an explicit yes fails closed: a client that errors, or never
// answers, applies nothing.
func (t *mcpTools) confirm(c *mcp.Call, a *approval) func(context.Context, *review) (bool, error) {
	return func(ctx context.Context, r *review) (bool, error) {
		var b strings.Builder
		if err := a.showReview(&console{w: &b}, r); err != nil {
			return false, err
		}
		b.WriteString("\n" + a.question(r))
		text := plainText(b.String())
		if len(text) > maxConfirmText {
			return false, fmt.Errorf("the review is %d bytes, too long to show here; run `zakwas approve %s` in a terminal", len(text), a.p.ID)
		}
		ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
		defer cancel()
		resp, err := c.Elicit(ctx, text, map[string]any{
			"apply": map[string]any{"type": "boolean", "title": "Apply and commit", "description": "Apply the plan above and commit the diff to the config repo."},
		}, "apply")
		if err != nil {
			return false, err
		}
		return resp.Action == mcp.ActionAccept && resp.Content["apply"] == true, nil
	}
}

// plainText drops what a client could render as more than text or what
// hides text from the reader: control and format characters other than
// newline and tab.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if proposal.Hidden(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

type observers []engine.Observer

func (o observers) Start(s engine.Step) {
	for _, obs := range o {
		obs.Start(s)
	}
}

func (o observers) Done(s engine.Step, d time.Duration, err error) {
	for _, obs := range o {
		obs.Done(s, d, err)
	}
}

// mcpProgress reports each step twice, when it starts and when it ends, so
// the progress value grows with every notification.
type mcpProgress struct{ c *mcp.Call }

func (p mcpProgress) Start(s engine.Step) {
	p.c.Progress(float64(2*s.N-1), float64(2*s.Total), fmt.Sprintf("[%d/%d] %s %s", s.N, s.Total, s.Module, plainText(s.Change.Target)))
}

func (p mcpProgress) Done(s engine.Step, d time.Duration, err error) {
	result := "done in " + round(d)
	if err != nil {
		result = "failed"
	}
	p.c.Progress(float64(2*s.N), float64(2*s.Total), fmt.Sprintf("[%d/%d] %s %s: %s", s.N, s.Total, s.Module, plainText(s.Change.Target), result))
}
