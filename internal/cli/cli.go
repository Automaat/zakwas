// Package cli wires config, modules and the engine behind the zakwas commands.
package cli

import (
	"bufio"
	"cmp"
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

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/lock"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/modules/brew"
	"github.com/Automaat/zakwas/internal/modules/commands"
	"github.com/Automaat/zakwas/internal/modules/defaults"
	"github.com/Automaat/zakwas/internal/modules/files"
	"github.com/Automaat/zakwas/internal/modules/launchd"
	"github.com/Automaat/zakwas/internal/modules/links"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/modules/system"
	"github.com/Automaat/zakwas/internal/modules/templates"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/schema"
)

// commandList lists every command in usage order; completion reads it too.
var commandList = []struct{ name, args, help string }{
	{"plan", "", "show pending changes"},
	{"apply", "", "show pending changes, confirm, apply them"},
	{"upgrade", "", "refresh Homebrew and agent marketplaces, then apply (picks up new versions)"},
	{"check", "[--offline]", "exit 2 when anything drifted (for CI/cron, agent hooks)"},
	{"capture", "[--write]", "list what agents have that zakwas.yaml doesn't declare; --write saves it as a proposal"},
	{"propose", "OPERATION [ARGS]...", "save a config change as a proposal, without editing anything"},
	{"proposals", "", "list pending proposals"},
	{"approve", "ID", "show a proposal's diff and plan, confirm, apply it and commit it"},
	{"mcp", "[--enable-apply]", "serve config reads and proposals to coding agents over MCP"},
	{"init", "DIR [--add PATH]...", "create a starter config repo in DIR from this Mac"},
	{"self-update", "[--version X.Y.Z]", "replace this zakwas binary with a release"},
	{"completion", "zsh|bash|fish", "print a shell completion script"},
	{"schema", "", "print the JSON Schema for zakwas.yaml (for editor validation)"},
	{"version", "", "print the zakwas version"},
}

var commandHelpOther = map[string]string{
	"upgrade": "refresh agent marketplaces, then apply (picks up new versions)",
}

func commandHelp(name, goos string) string {
	for _, c := range commandList {
		if c.name != name {
			continue
		}
		if goos != platform.Darwin {
			if help, ok := commandHelpOther[name]; ok {
				return help
			}
		}
		return strings.ReplaceAll(c.help, "this Mac", "this "+platform.Machine(goos))
	}
	return ""
}

func usage(goos string) string {
	var b strings.Builder
	b.WriteString("zakwas converges this " + platform.Machine(goos) + " to zakwas.yaml.\n\nUsage:\n  zakwas [flags] <command>\n\nCommands:\n")
	for _, c := range commandList {
		name := strings.TrimSpace(c.name + " " + c.args)
		fmt.Fprintf(&b, "  %-30s %s\n", name, commandHelp(c.name, goos))
	}
	b.WriteString("\nFlags (plan, apply, upgrade, check):\n")
	return b.String()
}

// LockHeldEnv is set for the commands an apply or upgrade runs, so a nested
// zakwas apply fails fast instead of waiting on its parent's lock.
const LockHeldEnv = "ZAKWAS_LOCK_HELD"

// Exit codes.
const (
	ExitOK    = 0
	ExitErr   = 1
	ExitDrift = 2
	ExitUsage = 64
)

// Env is everything the CLI takes from the outside world, so tests can run
// it hermetically.
type Env struct {
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Home    string
	Cwd     string
	UID     int
	Runner  runner.Runner
	PAMFile string
	Now     func() time.Time
	// Set by main from the real terminal and environment.
	StdinTTY      bool
	Color         bool
	GitHubActions bool
	Version       string
	Host          string
	// Executable is the binary self-update replaces (default: this one).
	Executable string
	// ReleaseURL overrides where self-update downloads releases (tests).
	ReleaseURL      string
	ClaudeConfigDir string
	CodexHome       string
	CopilotHome     string
	GeminiHome      string
	// LockHeldBy is $ZAKWAS_LOCK_HELD: the pid of the zakwas run this
	// process was started from, which holds the run lock.
	LockHeldBy string
	// GOOS is the operating system modules and config are checked against;
	// empty means the one zakwas runs on (tests set it).
	GOOS string
	// AgentSession names the coding agent this run was started from, from
	// DetectAgentSession; empty for a person's terminal.
	AgentSession string
}

func (e Env) goos() string { return cmp.Or(e.GOOS, platform.Current()) }

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Main runs zakwas and returns the process exit code.
func Main(ctx context.Context, env Env) int {
	out := &console{w: env.Stdout}
	errOut := &console{w: env.Stderr}
	code := run(ctx, env, out, errOut)
	if code == ExitOK && (out.err != nil || errOut.err != nil) {
		return ExitErr
	}
	return code
}

func run(ctx context.Context, env Env, out, errOut *console) int {
	if len(env.Args) > 0 {
		switch env.Args[0] {
		case "init":
			return runInit(ctx, env, env.Args[1:], out, errOut)
		case "self-update":
			return runSelfUpdate(ctx, env, env.Args[1:], out, errOut)
		case "completion":
			return runCompletion(env.Args[1:], out, errOut)
		case "capture":
			return runCapture(ctx, env, env.Args[1:], out, errOut)
		case "propose":
			return runPropose(ctx, env, env.Args[1:], out, errOut)
		case "proposals":
			return runProposals(env, env.Args[1:], out, errOut)
		case "approve":
			return runApprove(ctx, env, env.Args[1:], out, errOut)
		case "mcp":
			return runMCP(ctx, env, env.Args[1:], out, errOut)
		}
	}
	fs, o := newFlags(errOut)
	var cmd string
	if code, ok := parseCommand(fs, out, func() (err error) {
		cmd, err = parseArgs(fs, env.Args)
		return err
	}); !ok {
		return code
	}
	if cmd == "schema" {
		_, _ = out.Write(schema.JSON)
		return ExitOK
	}
	if !slices.Contains([]string{"plan", "apply", "upgrade", "check"}, cmd) {
		errOut.printf("unknown command %q\n", cmd)
		fs.Usage()
		return ExitUsage
	}
	if msg := flagConflict(cmd, o); msg != "" {
		errOut.print("zakwas: " + msg + "\n")
		return ExitUsage
	}
	if o.offline {
		env.Runner = offlineRunner{}
	}

	cfg, err := loadConfig(o.cfgPath, env)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if (cmd == "apply" || cmd == "upgrade") && env.AgentSession != "" && cfg.Agents.GuardEnabled() {
		errOut.printf("zakwas: refusing to %s from a %s session: an agent that can apply the config can widen its own permissions, add hooks or install marketplaces.\n"+
			"Review with `zakwas plan`, then run `zakwas %s` in a terminal outside the agent, or set agents.guard: false in %s\n", cmd, env.AgentSession, cmd, cfg.Path)
		return ExitErr
	}
	var saved *planDoc
	if o.planFile != "" {
		doc, err := loadPlan(o.planFile)
		if err != nil {
			errOut.fail(err)
			return ExitErr
		}
		saved = &doc
		o.only = strings.Join(doc.Only, ",")
	}
	mods, err := selectModules(Modules(cfg, env), o.only, env.goos())
	if err == nil && o.offline {
		mods, err = offlineModules(mods, o.only != "")
	}
	if err != nil {
		errOut.fail(err)
		return ExitUsage
	}
	if o.asJSON {
		toolOutputToStderr(env)
	}

	if cmd == "apply" || cmd == "upgrade" {
		held, code, ok := acquireLock(ctx, env, cmd, o.noWait, errOut)
		if !ok {
			return code
		}
		defer func() { _ = held.Release() }()
		cleanSelfUpdateLeftovers(env, env.goos(), errOut)
	}

	if cmd == "upgrade" {
		if code, ok := refresh(ctx, env, cfg, mods, errOut); !ok {
			return code
		}
	}

	style := engine.Style{Color: env.Color && !o.noColor && !o.asJSON, ShowDiffs: o.diff}
	plan := engine.Build(ctx, mods)
	planErr := plan.Err()
	var onlyList []string
	if o.only != "" {
		onlyList = strings.Split(o.only, ",")
	}
	machine := o.asJSON || o.outFile != "" || saved != nil
	var commit string
	if machine && !o.offline {
		commit, _ = git(ctx, env.Runner, cfg.Root, "rev-parse", "HEAD")
	}
	full := newPlanDoc(env, cfg.Path, commit, onlyList, plan, true)
	if saved != nil {
		if err := checkSaved(*saved, full); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}
	if o.outFile != "" {
		if err := savePlan(o.outFile, full); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}
	doc := newPlanDoc(env, cfg.Path, commit, onlyList, plan, o.diff)
	if o.offline {
		doc.Offline, doc.Skipped = true, offlineNotChecked(mods, env.goos())
	}
	switch {
	case o.asJSON && cmd == "plan", o.asJSON && cmd == "check":
		out.print(mustIndent(doc))
	case o.asJSON:
		writeJSON(out, event{Type: "plan", FormatVersion: engine.FormatVersion, Plan: &doc})
	default:
		if err := engine.Render(out, plan, style); err != nil {
			return ExitErr
		}
		if o.offline {
			out.print(offlineNote(style, offlineNotChecked(mods, env.goos())))
		}
	}

	switch cmd {
	case "plan":
		if planErr != nil {
			return ExitErr
		}
		return ExitOK
	case "check":
		switch {
		case planErr != nil:
			return ExitErr
		case plan.Empty():
			return ExitOK
		}
		return ExitDrift
	}

	if plan.Empty() {
		if o.asJSON {
			writeJSON(out, event{Type: "summary", Result: &resultJSON{}})
		}
		if planErr != nil {
			errOut.fail(planErr)
			return ExitErr
		}
		return ExitOK
	}
	for _, w := range repoWarnings(ctx, env.Runner, cfg.Root) {
		errOut.print("zakwas: warning: " + w + "\n")
	}
	if !o.yes && saved == nil {
		if !env.StdinTTY {
			errOut.print("zakwas: stdin is not a terminal, so apply can't ask for confirmation; review with `zakwas plan`, then run `zakwas apply -y`\n")
			return ExitUsage
		}
		if !confirm(env.Stdin, out, plan.Steps()) {
			out.print("aborted\n")
			return ExitErr
		}
	}

	var obs engine.Observer = jsonEvents{out: out}
	if !o.asJSON {
		out.print("\n")
		obs = &progress{out: out, style: style, groups: env.GitHubActions}
	}
	result, applyErr := engine.Apply(ctx, obs, plan)
	finalErr := errors.Join(planErr, applyErr)
	if err := recordHistory(ctx, env, cfg.Root, plan, result, finalErr); err != nil {
		errOut.print("zakwas: warning: history not recorded: " + err.Error() + "\n")
	}
	if o.asJSON {
		e := event{Type: "summary", Result: newResultJSON(result)}
		if finalErr != nil {
			e.Error = finalErr.Error()
		}
		writeJSON(out, e)
	} else {
		out.print("\n" + recap(result, style) + "\n")
	}
	if finalErr != nil {
		errOut.fail(finalErr)
		return ExitErr
	}
	return ExitOK
}

// acquireLock serializes the commands that change the system: a second one
// waits for the first, so it plans against what the first left behind. plan
// and check take no lock; state files are replaced atomically, so they never
// read a half-written one. The commands the run starts are marked as running
// under it whether or not the lock could be taken, so a nested zakwas apply is
// refused even in a state dir the lock can't live in.
func acquireLock(ctx context.Context, env Env, cmd string, noWait bool, errOut *console) (*lock.Lock, int, bool) {
	held, ok := takeLock(ctx, env, cmd, noWait, errOut)
	if !ok {
		return nil, ExitErr, false
	}
	if ex, ok := env.Runner.(*runner.Exec); ok {
		ex.Env = append(ex.Env, LockHeldEnv+"="+strconv.Itoa(os.Getpid()))
	}
	return held, ExitOK, true
}

// takeLock takes the run lock without marking the runner's commands as
// running under it, for a long-lived process that sets that once. A lock the
// state dir or file won't allow is a warning, not a failure, so a read-only
// state dir keeps working as it did before the lock; the returned lock is
// then nil.
func takeLock(ctx context.Context, env Env, cmd string, noWait bool, errOut *console) (*lock.Lock, bool) {
	if env.LockHeldBy != "" {
		errOut.printf("zakwas: can't run %s inside a zakwas run (pid %s holds the lock and would wait for itself forever)\n", cmd, env.LockHeldBy)
		return nil, false
	}
	held, err := lock.Acquire(ctx, env.Home, "zakwas "+cmd, lock.Options{
		NoWait: noWait,
		OnWait: func(h lock.Holder) {
			errOut.print("zakwas: waiting for " + h.String() + " to finish\n")
		},
	})
	var unavailable *lock.UnavailableError
	if errors.As(err, &unavailable) {
		errOut.print("zakwas: warning: " + unavailable.Error() + "; running without it, so another run is not kept out\n")
		return nil, true
	}
	if err != nil {
		if ctx.Err() == nil {
			errOut.fail(err)
		}
		return nil, false
	}
	return held, true
}

// refresh fetches new package and plugin versions for `zakwas upgrade`:
// Homebrew's package list and the declared agent marketplaces, for the
// selected modules. plan and apply never fetch.
func refresh(ctx context.Context, env Env, cfg *config.Config, mods []engine.Module, errOut *console) (int, bool) {
	var brewSelected bool
	var agentsMod *agents.Module
	for _, m := range mods {
		switch m := m.(type) {
		case *brew.Module:
			brewSelected = true
		case *agents.Module:
			agentsMod = m
		}
	}
	if cfg.Brew == nil && cfg.Agents == nil {
		sections := "a brew or agents section"
		if !platform.Supported("brew", env.goos()) {
			sections = "an agents section"
		}
		errOut.print("zakwas: upgrade needs " + sections + " in zakwas.yaml\n")
		return ExitUsage, false
	}
	if brewSelected {
		update := runner.Cmd{Name: "brew", Args: []string{"update", "--quiet"}, Stream: true}
		if err := runner.Check(ctx, env.Runner, update); err != nil {
			errOut.fail(err)
			return ExitErr, false
		}
	}
	if agentsMod != nil {
		if err := agentsMod.Refresh(ctx); err != nil {
			errOut.fail(err)
			return ExitErr, false
		}
	}
	return ExitOK, true
}

type options struct {
	cfgPath, only, outFile, planFile string
	yes, diff, noColor, asJSON       bool
	noWait, offline                  bool
}

// newFlags defines the flags of plan, apply, upgrade and check.
func newFlags(errOut io.Writer) (*flag.FlagSet, *options) {
	var o options
	fs := flag.NewFlagSet("zakwas", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), usage(platform.Current()))
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	fs.StringVar(&o.only, "only", "", "comma-separated modules to run (default: all)")
	fs.BoolVar(&o.yes, "y", false, "apply without asking for confirmation")
	fs.BoolVar(&o.diff, "diff", false, "show file content diffs and command scripts")
	fs.BoolVar(&o.noColor, "no-color", false, "disable colors (also NO_COLOR)")
	fs.BoolVar(&o.asJSON, "json", false, "machine-readable output: a plan document, or JSON lines events for apply")
	fs.BoolVar(&o.noWait, "no-wait", false, "apply: exit 1 at once when another zakwas run holds the lock, instead of waiting for it")
	fs.BoolVar(&o.offline, "offline", false, "check: only compare the files zakwas owns with its recorded state, starting no command (fast; skips "+strings.Join(offlineSkippedOn(platform.Current()), ", ")+")")
	fs.StringVar(&o.outFile, "out", "", "plan: also save the plan to this file, for apply -plan")
	fs.StringVar(&o.planFile, "plan", "", "apply: apply this saved plan if the machine still matches it, without prompting")
	return fs, &o
}

// flagConflict rejects flag combinations that would silently do the wrong
// thing, e.g. a JSON consumer stuck at a confirmation prompt.
func flagConflict(cmd string, o *options) string {
	apply := cmd == "apply" || cmd == "upgrade"
	asJSON, yes, outFile, planFile, only := o.asJSON, o.yes, o.outFile, o.planFile, o.only
	switch {
	case o.noWait && !apply:
		return "-no-wait only works with apply and upgrade"
	case o.offline && cmd != "check":
		return "-offline only works with check"
	case outFile != "" && cmd != "plan":
		return "-out only works with plan"
	case planFile != "" && cmd != "apply":
		return "-plan only works with apply"
	case planFile != "" && only != "":
		return "-plan applies the modules saved in the plan; drop --only"
	case asJSON && apply && !yes && planFile == "":
		return "apply --json can't prompt; pass -y or -plan"
	}
	return ""
}

func mustIndent(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}

// toolOutputToStderr keeps stdout pure JSON: brew and mise output streamed
// by apply goes to stderr instead.
func toolOutputToStderr(env Env) {
	if ex, ok := env.Runner.(*runner.Exec); ok {
		ex.Stdout = ex.Stderr
	}
}

// console remembers the first write error so output failures (closed pipe,
// full disk) turn into a failing exit code instead of vanishing.
type console struct {
	w   io.Writer
	err error
}

func (c *console) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.err = err
	return n, err
}

func (c *console) print(s string) {
	_, _ = io.WriteString(c, s)
}

func (c *console) printf(format string, args ...any) {
	c.print(fmt.Sprintf(format, args...))
}

func (c *console) fail(err error) {
	c.print("zakwas: " + err.Error() + "\n")
}

// parseArgs accepts flags on either side of the command, so both
// `zakwas -y apply` and `zakwas apply -y` work.
func parseArgs(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() == 0 {
		return "", errUsage
	}
	cmd := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", errUsage
	}
	return cmd, nil
}

var errUsage = errors.New("usage")

// parseCommand runs parse with the flag set's usage silenced, then prints
// it once: to stdout with exit 0 for -h/--help, to stderr with exit 64 for
// bad arguments (after the flag package's own error line, if any).
func parseCommand(fs *flag.FlagSet, out *console, parse func() error) (int, bool) {
	usage := fs.Usage
	fs.Usage = func() {}
	err := parse()
	fs.Usage = usage
	switch {
	case err == nil:
		return ExitOK, true
	case errors.Is(err, flag.ErrHelp):
		fs.SetOutput(out)
		usage()
		return ExitOK, false
	}
	usage()
	return ExitUsage, false
}

func loadConfig(flagPath string, env Env) (*config.Config, error) {
	path := flagPath
	if path == "" {
		found, err := config.Find(env.Cwd)
		switch {
		case err == nil:
			path = found
		case os.Getenv("ZAKWAS_CONFIG") != "":
			path = os.Getenv("ZAKWAS_CONFIG")
		default:
			return nil, err
		}
	}
	return config.LoadFor(path, env.Home, env.GOOS)
}

// Modules returns every configured module in apply order. Order matters:
// files put the mise config in place, brew installs mise, agents need the
// agent CLIs brew or mise install, commands may need tools from any, and
// launchd jobs may run what commands set up.
func Modules(cfg *config.Config, env Env) []engine.Module {
	goos := env.goos()
	here := func(module string) bool { return platform.Supported(module, goos) }
	paths := config.Paths{Home: env.Home, Root: cfg.Root}
	immutable := cfg.Protect.Immutable && platform.FeatureSupported("protect.immutable", goos)
	installer := &install.Installer{Paths: paths, StatePath: install.StatePath(env.Home), Immutable: immutable}
	var mods []engine.Module
	if here("system") {
		mods = append(mods, &system.Module{System: cfg.System, Paths: paths, Runner: env.Runner, PAMFile: env.PAMFile})
	}
	mods = append(mods, &files.Module{Files: cfg.Files, Keep: templateDsts(cfg, paths), Paths: paths, Installer: installer, Immutable: cfg.Protect.Immutable, GOOS: goos})
	if here("links") {
		mods = append(mods, &links.Module{Links: cfg.Links, Paths: paths})
	}
	mods = append(mods, &templates.Module{Templates: cfg.Templates, Agents: cfg.Agents, Paths: paths, Installer: installer, GOOS: goos})
	if cfg.Brew != nil && here("brew") {
		mods = append(mods, &brew.Module{Brew: *cfg.Brew, Paths: paths, Runner: env.Runner})
	}
	if cfg.Mise != nil && here("mise") {
		mods = append(mods, &mise.Module{Mise: *cfg.Mise, Paths: paths, Runner: env.Runner})
	}
	agentsCfg := cfg.Agents
	for _, state := range []string{agents.InstructionsStatePath(env.Home), agents.InstructionFilesStatePath(env.Home)} {
		if _, err := os.Lstat(state); agentsCfg == nil && err == nil {
			agentsCfg = &config.Agents{}
		}
	}
	if _, err := os.Lstat(agents.CommandHooksStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.PermissionsStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.MCPStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.ProfilesStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.SkillsStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.GeminiStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if _, err := os.Lstat(agents.HookFilesStatePath(env.Home)); agentsCfg == nil && err == nil {
		agentsCfg = &config.Agents{}
	}
	if agentsCfg != nil {
		mods = append(mods, &agents.Module{
			Agents: *agentsCfg, Paths: paths, Runner: env.Runner,
			Guard: cfg.Agents.GuardEnabled(), GOOS: goos, TemplateSources: templateSrcs(cfg, paths),
			Vars: cfg.Templates.Vars, Immutable: immutable, TemplateFiles: cfg.Templates.Files,
			Destinations:    cfg.Destinations(paths),
			ClaudeConfigDir: absFromCwd(env, env.ClaudeConfigDir), CodexHome: absFromCwd(env, env.CodexHome),
			GeminiHome: absFromCwd(env, env.GeminiHome), CopilotHome: absFromCwd(env, env.CopilotHome),
		})
	}
	if here("commands") {
		mods = append(mods, &commands.Module{Commands: cfg.Commands, Home: env.Home, Runner: env.Runner})
	}
	if _, err := os.Lstat(launchd.StatePath(env.Home)); here("launchd") && (len(cfg.Launchd) > 0 || err == nil) {
		mods = append(mods, &launchd.Module{Jobs: cfg.Launchd, Paths: paths, Runner: env.Runner, UID: env.UID})
	}
	if here("defaults") {
		mods = append(mods, &defaults.Module{Defaults: cfg.Defaults, Runner: env.Runner})
	}
	return mods
}

// absFromCwd makes an agent's config dir ($CLAUDE_CONFIG_DIR, $CODEX_HOME,
// $GEMINI_CLI_HOME, $COPILOT_HOME) absolute: zakwas reads it from its own working
// directory but runs the agent from /.
func absFromCwd(env Env, dir string) string {
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(env.Cwd, dir)
}

func templateDsts(cfg *config.Config, paths config.Paths) []string {
	var dsts []string
	for _, t := range cfg.Templates.Files {
		dsts = append(dsts, paths.Dst(t.Dst))
	}
	return dsts
}

func templateSrcs(cfg *config.Config, paths config.Paths) []string {
	var srcs []string
	for _, t := range cfg.Templates.Files {
		srcs = append(srcs, paths.Src(t.Src))
	}
	return srcs
}

func selectModules(all []engine.Module, only, goos string) ([]engine.Module, error) {
	if only == "" {
		return all, nil
	}
	byName := map[string]engine.Module{}
	for _, m := range all {
		byName[m.Name()] = m
	}
	var wanted []string
	var errs []error
	for name := range strings.SplitSeq(only, ",") {
		name = strings.TrimSpace(name)
		if _, ok := byName[name]; !ok {
			if err := platform.ModuleError(name, goos); err != nil {
				errs = append(errs, err)
			} else {
				errs = append(errs, fmt.Errorf("unknown or unconfigured module %q", name))
			}
		}
		wanted = append(wanted, name)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var out []engine.Module
	for _, m := range all {
		if slices.Contains(wanted, m.Name()) {
			out = append(out, m)
		}
	}
	return out, nil
}

func confirm(stdin io.Reader, out *console, n int) bool {
	out.printf("\nApply %d step(s)? [y/N] ", n)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
