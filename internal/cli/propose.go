package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/proposal"
)

const proposeUsageText = `Usage: zakwas propose OPERATION [flags]

Saves a change to the config repo as a proposal, with its plan and risk tier.
Nothing is edited: review it with 'zakwas proposals' and apply it with
'zakwas approve ID' in a terminal.

Operations:
  batch FILE (JSON array of operations; - reads stdin)
  add-marketplace NAME SOURCE [--providers claude,codex]
  add-plugin NAME@MARKETPLACE [--providers claude,codex]
  add-permission allow|review|deny COMMAND... [--providers ...] [--justification TEXT]
  remove-permission allow|review|deny COMMAND...
  add-brew NAME [--cask]
  pin-mise TOOL VERSION
  add-mcp NAME (--command CMD [--arg A]... [--env-var NAME]... | --url URL [--bearer-token-env NAME | --oauth-client-id ID [--oauth-callback-port PORT] [--oauth-scope SCOPES]]) [--providers ...]

A change goes to the main config's repo unless --layer names a local layer,
whose repo it goes to instead. An edit or removal goes to the file that holds
the entry. A proposal is committed in one repo.

Flags:
`

func proposeUsage(goos string) string {
	var out []string
	for line := range strings.SplitAfterSeq(proposeUsageText, "\n") {
		kind := strings.Fields(line)
		if len(kind) > 0 && proposal.KindError(kind[0], goos) != nil {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "")
}

// printDefaultsExcept is PrintDefaults without the flag named hidden, for a
// flag that stays defined but does nothing on this system.
func printDefaultsExcept(fs *flag.FlagSet, hidden string) {
	shown := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
	shown.SetOutput(fs.Output())
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == hidden {
			return
		}
		shown.Var(f.Value, f.Name, f.Usage)
		shown.Lookup(f.Name).DefValue = f.DefValue
	})
	shown.PrintDefaults()
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(s string) error {
	*l = append(*l, s)
	return nil
}

type proposeOptions struct {
	cfgPath        string
	layer          string
	asJSON         bool
	providers      string
	justification  string
	cask           bool
	command        string
	url            string
	bearerTokenEnv string
	args, envVars  listFlag
	oauthClientID  string
	oauthPort      int
	oauthScope     string
}

func newProposeFlags(errOut io.Writer) (*flag.FlagSet, *proposeOptions) {
	var o proposeOptions
	fs := flag.NewFlagSet("zakwas propose", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), proposeUsage(platform.Current()))
		hidden := ""
		if !platform.Supported("brew", platform.Current()) {
			hidden = "cask"
		}
		printDefaultsExcept(fs, hidden)
	}
	fs.StringVar(&o.cfgPath, "c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	fs.BoolVar(&o.asJSON, "json", false, "print the proposal as JSON")
	fs.StringVar(&o.layer, "layer", "", "name of the local layer whose repo the change goes to (default: the main config's)")
	fs.StringVar(&o.providers, "providers", "", "comma-separated providers the entry targets (default: the section's)")
	fs.StringVar(&o.justification, "justification", "", "add-permission: one-line reason Codex shows for a prompting or forbidden command")
	fs.BoolVar(&o.cask, "cask", false, "add-brew: the package is a cask")
	fs.StringVar(&o.command, "command", "", "add-mcp: command that starts a stdio server")
	fs.StringVar(&o.url, "url", "", "add-mcp: URL of an HTTP server")
	fs.StringVar(&o.bearerTokenEnv, "bearer-token-env", "", "add-mcp: environment variable holding the bearer token of a url server")
	fs.StringVar(&o.oauthClientID, "oauth-client-id", "", "add-mcp: pre-registered OAuth client ID of a url server (public; a client secret is not supported)")
	fs.IntVar(&o.oauthPort, "oauth-callback-port", 0, "add-mcp: fixed port of the OAuth callback, with --oauth-client-id")
	fs.StringVar(&o.oauthScope, "oauth-scope", "", "add-mcp: OAuth scopes separated by spaces, with --oauth-client-id")
	fs.Var(&o.args, "arg", "add-mcp: argument of the command (repeatable)")
	fs.Var(&o.envVars, "env-var", "add-mcp: environment variable passed through to the command (repeatable)")
	return fs, &o
}

type configOptions struct {
	cfgPath string
	asJSON  bool
}

func newConfigFlags(name, usage string, withJSON bool, errOut io.Writer) (*flag.FlagSet, *configOptions) {
	var o configOptions
	fs := flag.NewFlagSet("zakwas "+name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), usage)
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	if withJSON {
		fs.BoolVar(&o.asJSON, "json", false, "machine-readable output")
	}
	return fs, &o
}

const proposalsUsage = "Usage: zakwas proposals [-c zakwas.yaml] [-json]\n\nLists the pending proposals for this config.\n\nFlags:\n"

const approveUsage = `Usage: zakwas approve ID [-c zakwas.yaml]

Shows the diff and plan of a proposal, asks, applies only the modules it
affects and commits the edited files to the current branch of the config
repo without pushing. Needs a terminal. Refuses a proposal whose base config
changed, that is older than 7 days, or while an edited file has uncommitted
changes.

Flags:
`

// parseInterleaved accepts flags on either side of the positional arguments.
// Everything after a bare -- is positional, for commands that start with a
// dash.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	if i := slices.Index(args, "--"); i >= 0 {
		args, tail = args[:i], args[i+1:]
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return append(pos, tail...), nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// parseSub parses a subcommand's arguments with its usage silenced, then
// prints it once like parseCommand does.
func parseSub(fs *flag.FlagSet, out *console, args []string) (pos []string, code int, ok bool) {
	code, ok = parseCommand(fs, out, func() (err error) {
		pos, err = parseInterleaved(fs, args)
		return err
	})
	return pos, code, ok
}

var opFlags = map[string][]string{
	proposal.AddMarketplace:   {"layer", "providers"},
	proposal.AddPlugin:        {"layer", "providers"},
	proposal.AddPermission:    {"layer", "providers", "justification"},
	proposal.RemovePermission: {"layer"},
	proposal.AddBrew:          {"layer", "cask"},
	proposal.PinMise:          {"layer"},
	proposal.AddMCP:           {"layer", "providers", "command", "url", "bearer-token-env", "arg", "env-var", "oauth-client-id", "oauth-callback-port", "oauth-scope"},
}

func opFromArgs(fs *flag.FlagSet, o *proposeOptions, pos []string, goos string) (proposal.Op, error) {
	if len(pos) == 0 {
		return proposal.Op{}, errUsage
	}
	kind, rest := pos[0], pos[1:]
	allowed, known := opFlags[kind]
	if !known {
		return proposal.Op{}, fmt.Errorf("unknown operation %q; use one of %s", kind, strings.Join(proposal.KindsFor(goos), ", "))
	}
	if err := proposal.KindError(kind, goos); err != nil {
		return proposal.Op{}, err
	}
	var stray []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "c" && f.Name != "json" && !slices.Contains(allowed, f.Name) {
			stray = append(stray, "-"+f.Name)
		}
	})
	if len(stray) > 0 {
		return proposal.Op{}, fmt.Errorf("%s doesn't take %s", kind, strings.Join(stray, ", "))
	}
	op := proposal.Op{Kind: kind, Justification: o.justification, Cask: o.cask, Layer: o.layer}
	for p := range strings.SplitSeq(o.providers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			op.Providers = append(op.Providers, p)
		}
	}
	need := func(n int, form string) error {
		if len(rest) != n {
			return fmt.Errorf("usage: zakwas propose %s %s", kind, form)
		}
		return nil
	}
	switch kind {
	case proposal.AddPlugin, proposal.AddBrew, proposal.AddMCP:
		if err := need(1, "NAME"); err != nil {
			return op, err
		}
		op.Name = rest[0]
	case proposal.PinMise:
		if err := need(2, "TOOL VERSION"); err != nil {
			return op, err
		}
		op.Name, op.Version = rest[0], rest[1]
	case proposal.AddMarketplace:
		if err := need(2, "NAME SOURCE"); err != nil {
			return op, err
		}
		op.Name, op.Source = rest[0], rest[1]
	default:
		if len(rest) < 2 {
			return op, fmt.Errorf("%s needs a decision (allow, review or deny) and a command", kind)
		}
		op.Decision, op.Command = rest[0], strings.Join(rest[1:], " ")
	}
	if kind == proposal.AddMCP {
		op.Server = &proposal.Server{
			Command: o.command, Args: o.args, URL: o.url, EnvVars: o.envVars, BearerTokenEnv: o.bearerTokenEnv,
		}
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if set["oauth-scope"] && o.oauthScope == "" {
			return op, errors.New("--oauth-scope is empty; leave it out to request the server's default scopes")
		}
		if set["oauth-client-id"] || set["oauth-callback-port"] || set["oauth-scope"] {
			op.Server.OAuth = &proposal.OAuth{ClientID: o.oauthClientID, Scope: o.oauthScope}
			if set["oauth-callback-port"] {
				op.Server.OAuth.CallbackPort = &o.oauthPort
			}
		}
	}
	return op, op.Validate()
}

func proposalOps(fs *flag.FlagSet, o *proposeOptions, pos []string, env Env) ([]proposal.Op, error) {
	if len(pos) == 0 || pos[0] != "batch" {
		op, err := opFromArgs(fs, o, pos, env.goos())
		return []proposal.Op{op}, err
	}
	if len(pos) != 2 {
		return nil, errors.New("usage: zakwas propose batch FILE (JSON array; - reads stdin)")
	}
	var stray []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "c" && f.Name != "json" && f.Name != "layer" {
			stray = append(stray, "-"+f.Name)
		}
	})
	if len(stray) > 0 {
		return nil, fmt.Errorf("batch doesn't take %s; set operation fields in the JSON array", strings.Join(stray, ", "))
	}
	input := env.Stdin
	if pos[1] != "-" {
		f, err := os.Open(absFromCwd(env, pos[1]))
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		input = f
	}
	var ops []proposal.Op
	dec := json.NewDecoder(input)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ops); err != nil {
		return nil, fmt.Errorf("batch must be a JSON array of operations: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("batch must contain exactly one JSON array")
	}
	if err := proposal.DefaultLayer(ops, o.layer); err != nil {
		return nil, err
	}
	if err := proposal.ValidateOps(ops); err != nil {
		return nil, err
	}
	for _, op := range ops {
		if err := proposal.KindError(op.Kind, env.goos()); err != nil {
			return nil, err
		}
	}
	return ops, nil
}

func runPropose(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, o := newProposeFlags(errOut)
	pos, code, ok := parseSub(fs, out, args)
	if !ok {
		return code
	}
	ops, err := proposalOps(fs, o, pos, env)
	if err != nil {
		if errors.Is(err, errUsage) {
			fs.Usage()
		} else {
			errOut.fail(err)
		}
		return ExitUsage
	}
	cfg, err := loadConfig(o.cfgPath, env)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	p, ev, err := saveProposal(ctx, env, cfg, ops...)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if o.asJSON {
		out.print(mustIndent(p))
		return ExitOK
	}
	return printProposal(out, env, p, ev)
}

// printProposal shows a saved proposal: why it is rated as it is, its diff
// and the plan it will run.
func printProposal(out *console, env Env, p *proposal.Proposal, ev *evaluation) int {
	style := engine.Style{Color: env.Color, ShowDiffs: true}
	out.printf("Proposal %s: %s (%s risk)\n", p.ID, p.Summary, p.Tier)
	if p.Layer != "" {
		out.printf("  for layer %s: approve commits the edit in that layer's repo\n", printable(p.Layer))
	}
	for _, r := range p.Reasons {
		out.print("  " + r + "\n")
	}
	out.print("\n" + p.Diff + "\n\n")
	if err := engine.Render(out, ev.plan, style); err != nil {
		return ExitErr
	}
	out.printf("\nNothing was changed. Apply it from a terminal with: zakwas approve %s\n", p.ID)
	return ExitOK
}

// saveProposal rates ops against the config repo and saves them as one
// proposal.
func saveProposal(ctx context.Context, env Env, cfg *config.Config, ops ...proposal.Op) (*proposal.Proposal, *evaluation, error) {
	ev, err := evaluate(ctx, env, cfg, ops)
	if err != nil {
		return nil, nil, err
	}
	id, err := proposal.NewID()
	if err != nil {
		return nil, nil, err
	}
	p := &proposal.Proposal{
		FormatVersion: engine.FormatVersion, ID: id, Created: env.now().UTC().Truncate(time.Second),
		Config: cfg.Path, Layer: ev.layer, Summary: proposal.Summaries(ev.ops), Ops: ev.ops, Base: ev.base,
		Tier: ev.tier, Reasons: ev.reasons, Modules: ev.modules, Diff: ev.diff, Plan: ev.plan.JSONModules(false),
	}
	if err := (proposal.Store{Dir: proposal.Dir(env.Home)}).Save(p); err != nil {
		return nil, nil, err
	}
	return p, ev, nil
}

func runProposals(env Env, args []string, out, errOut *console) int {
	fs, o := newConfigFlags("proposals", proposalsUsage, true, errOut)
	pos, code, ok := parseSub(fs, out, args)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fs.Usage()
		return ExitUsage
	}
	cfg, err := loadConfig(o.cfgPath, env)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	all, err := (proposal.Store{Dir: proposal.Dir(env.Home)}).List()
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	mine := slices.DeleteFunc(all, func(p *proposal.Proposal) bool { return p.Config != cfg.Path })
	if o.asJSON {
		if mine == nil {
			mine = []*proposal.Proposal{}
		}
		out.print(mustIndent(struct {
			FormatVersion string               `json:"format_version"`
			Proposals     []*proposal.Proposal `json:"proposals"`
		}{engine.FormatVersion, mine}))
		return ExitOK
	}
	if len(mine) == 0 {
		out.print("No pending proposals\n")
		return ExitOK
	}
	now := env.now()
	for _, p := range mine {
		out.printf("%s  %-4s  %-8s  %s%s\n", p.ID, printable(p.Tier), expiry(p, now), printable(p.Summary), layerNote(p))
	}
	return ExitOK
}

// layerNote marks a proposal that goes to a layer's repo.
func layerNote(p *proposal.Proposal) string {
	if p.Layer == "" {
		return ""
	}
	return " (layer " + printable(p.Layer) + ")"
}

func expiry(p *proposal.Proposal, now time.Time) string {
	if p.Expired(now) {
		return "expired"
	}
	left := p.Expires().Sub(now)
	if days := (left + 24*time.Hour - 1) / (24 * time.Hour); left > 24*time.Hour {
		return "in " + strconv.Itoa(int(days)) + "d"
	}
	return "in " + strconv.Itoa(int((left+time.Hour-1)/time.Hour)) + "h"
}

// evaluation is what a proposal's operations do to the current config repo:
// the edits, the plan of the modules they affect, and the tier of both.
type evaluation struct {
	// layer names the local layer whose repo the edits belong to, empty for
	// the main config's; root is that repo's directory.
	layer, root string
	ops         []proposal.Op
	edits       []proposal.Edit
	base        map[string]string
	modules     []string
	plan        engine.Plan
	tier        string
	reasons     []string
	diff        string
}

// evaluate applies ops in memory and plans the affected modules against the
// edited files, which are put in a temporary directory so the repo stays
// untouched.
func evaluate(ctx context.Context, env Env, cfg *config.Config, ops []proposal.Op) (*evaluation, error) {
	res, err := proposal.Apply(ops, cfg, env.Home)
	if err != nil {
		return nil, err
	}
	ev := &evaluation{ops: ops, edits: res.Edits, base: map[string]string{}, layer: res.Layer, root: res.Root}
	var diffs []string
	for _, e := range res.Edits {
		ev.base[e.Rel] = proposal.Hash(e.Old)
		diffs = append(diffs, udiff.Unified("a/"+e.Rel, "b/"+e.Rel, string(e.Old), string(e.New)))
	}
	ev.diff = strings.TrimRight(strings.Join(diffs, "\n"), "\n")

	dir, err := os.MkdirTemp("", "zakwas-propose-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	planCfg, err := configWithEdits(res, dir, env.Home)
	if err != nil {
		return nil, err
	}
	mods := Modules(planCfg, env)
	names := proposal.ModulesOf(ops, ModuleNames)
	names = slices.DeleteFunc(names, func(n string) bool {
		return !slices.ContainsFunc(mods, func(m engine.Module) bool { return m.Name() == n })
	})
	ev.modules = names
	if len(names) > 0 {
		selected, err := selectModules(mods, strings.Join(names, ","), env.goos())
		if err != nil {
			return nil, err
		}
		ev.plan = engine.Build(ctx, selected)
	}
	ev.tier, ev.reasons = rate(env, ops, ev.plan)
	return ev, nil
}

// rate is the proposal's tier: low only when every operation is low risk and
// the plan it will run is complete and stays away from sensitive paths.
func rate(env Env, ops []proposal.Op, plan engine.Plan) (string, []string) {
	tier, reasons := proposal.Classify(ops)
	sens := proposal.Sensitive{Home: env.Home, Extra: []string{absFromCwd(env, env.ClaudeConfigDir), absFromCwd(env, env.CodexHome), absFromCwd(env, env.GeminiHome), absFromCwd(env, env.CopilotHome), opencodeConfigDir(env)}}
	for _, mp := range plan {
		if mp.Err != nil {
			reasons = append(reasons, fmt.Sprintf("the %s plan is incomplete: %v", mp.Module, mp.Err))
		}
		for _, c := range mp.Changes {
			if path, hit := sens.Touches(c.Target + " " + c.Detail); hit {
				reasons = append(reasons, fmt.Sprintf("the plan changes %s (under %s)", c.Target, path))
			}
		}
	}
	if len(reasons) > 0 {
		return proposal.TierHigh, reasons
	}
	return tier, nil
}

// configWithEdits is the edited config with every use of an edited repo file
// pointing at a copy of the new contents in dir.
func configWithEdits(res *proposal.Result, dir, home string) (*config.Config, error) {
	c := *res.Config
	repl := map[string]string{}
	for i, e := range res.Edits {
		if e.Path == c.Path {
			continue
		}
		tmp := filepath.Join(dir, strconv.Itoa(i)+"-"+filepath.Base(e.Path))
		if err := os.WriteFile(tmp, e.New, 0o600); err != nil {
			return nil, err
		}
		repl[e.Path] = tmp
	}
	paths := config.Paths{Home: home, Root: c.Root}
	sub := func(src string) string {
		if tmp, ok := repl[paths.Src(src)]; ok {
			return tmp
		}
		return src
	}
	c.Files = slices.Clone(c.Files)
	for i := range c.Files {
		c.Files[i].Src = sub(c.Files[i].Src)
	}
	c.Links = slices.Clone(c.Links)
	for i := range c.Links {
		c.Links[i].Src = sub(c.Links[i].Src)
	}
	c.Templates.Files = slices.Clone(c.Templates.Files)
	for i := range c.Templates.Files {
		c.Templates.Files[i].Src = sub(c.Templates.Files[i].Src)
	}
	if c.Brew != nil {
		b := *c.Brew
		b.File = sub(b.File)
		b.Layers = slices.Clone(b.Layers)
		for i := range b.Layers {
			b.Layers[i].File = sub(b.Layers[i].File)
		}
		c.Brew = &b
	}
	c.RemapLayerMise(sub)
	if c.Mise != nil {
		m := *c.Mise
		m.Config = sub(m.Config)
		c.Mise = &m
	}
	return &c, nil
}

// printable drops control characters from text read back from a proposal
// file, which anything that can write the state directory could have forged.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if proposal.Hidden(r) {
			return -1
		}
		return r
	}, s)
}

// opencodeConfigDir is opencode's config home when $XDG_CONFIG_HOME moves it
// out of ~/.config, which the sensitive list already covers.
func opencodeConfigDir(env Env) string {
	home, _ := os.UserHomeDir()
	if xdg := os.Getenv("XDG_CONFIG_HOME"); env.Home == home && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "opencode")
	}
	return ""
}
