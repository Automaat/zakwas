// Package cli wires config, modules and the engine behind the zakwas commands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/modules/brew"
	"github.com/Automaat/zakwas/internal/modules/commands"
	"github.com/Automaat/zakwas/internal/modules/defaults"
	"github.com/Automaat/zakwas/internal/modules/files"
	"github.com/Automaat/zakwas/internal/modules/links"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/modules/system"
	"github.com/Automaat/zakwas/internal/modules/templates"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/schema"
)

// commandList lists every command in usage order; completion reads it too.
var commandList = []struct{ name, args, help string }{
	{"plan", "", "show pending changes"},
	{"apply", "", "show pending changes, confirm, apply them"},
	{"upgrade", "", "refresh Homebrew and agent marketplaces, then apply (picks up new versions)"},
	{"check", "", "exit 2 when anything drifted (for CI/cron)"},
	{"init", "DIR [--add PATH]...", "create a starter config repo in DIR from this Mac"},
	{"self-update", "[--version X.Y.Z]", "replace this zakwas binary with a release"},
	{"completion", "zsh|bash|fish", "print a shell completion script"},
	{"schema", "", "print the JSON Schema for zakwas.yaml (for editor validation)"},
	{"version", "", "print the zakwas version"},
}

func usage() string {
	var b strings.Builder
	b.WriteString("zakwas converges this Mac to zakwas.yaml.\n\nUsage:\n  zakwas [flags] <command>\n\nCommands:\n")
	for _, c := range commandList {
		name := strings.TrimSpace(c.name + " " + c.args)
		fmt.Fprintf(&b, "  %-30s %s\n", name, c.help)
	}
	b.WriteString("\nFlags (plan, apply, upgrade, check):\n")
	return b.String()
}

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
}

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
	if msg := flagConflict(cmd, o.asJSON, o.yes, o.outFile, o.planFile, o.only); msg != "" {
		errOut.print("zakwas: " + msg + "\n")
		return ExitUsage
	}

	cfg, err := loadConfig(o.cfgPath, env.Cwd, env.Home)
	if err != nil {
		errOut.fail(err)
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
	mods, err := selectModules(Modules(cfg, env), o.only)
	if err != nil {
		errOut.fail(err)
		return ExitUsage
	}
	if o.asJSON {
		toolOutputToStderr(env)
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
	if machine {
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
	switch {
	case o.asJSON && cmd == "plan", o.asJSON && cmd == "check":
		out.print(mustIndent(doc))
	case o.asJSON:
		writeJSON(out, event{Type: "plan", FormatVersion: engine.FormatVersion, Plan: &doc})
	default:
		if err := engine.Render(out, plan, style); err != nil {
			return ExitErr
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
		errOut.print("zakwas: upgrade needs a brew or agents section in zakwas.yaml\n")
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
}

// newFlags defines the flags of plan, apply, upgrade and check.
func newFlags(errOut io.Writer) (*flag.FlagSet, *options) {
	var o options
	fs := flag.NewFlagSet("zakwas", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), usage())
		fs.PrintDefaults()
	}
	fs.StringVar(&o.cfgPath, "c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	fs.StringVar(&o.only, "only", "", "comma-separated modules to run (default: all)")
	fs.BoolVar(&o.yes, "y", false, "apply without asking for confirmation")
	fs.BoolVar(&o.diff, "diff", false, "show file content diffs and command scripts")
	fs.BoolVar(&o.noColor, "no-color", false, "disable colors (also NO_COLOR)")
	fs.BoolVar(&o.asJSON, "json", false, "machine-readable output: a plan document, or JSON lines events for apply")
	fs.StringVar(&o.outFile, "out", "", "plan: also save the plan to this file, for apply -plan")
	fs.StringVar(&o.planFile, "plan", "", "apply: apply this saved plan if the machine still matches it, without prompting")
	return fs, &o
}

// flagConflict rejects flag combinations that would silently do the wrong
// thing, e.g. a JSON consumer stuck at a confirmation prompt.
func flagConflict(cmd string, asJSON, yes bool, outFile, planFile, only string) string {
	apply := cmd == "apply" || cmd == "upgrade"
	switch {
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

func loadConfig(flagPath, cwd, home string) (*config.Config, error) {
	path := flagPath
	if path == "" {
		found, err := config.Find(cwd)
		switch {
		case err == nil:
			path = found
		case os.Getenv("ZAKWAS_CONFIG") != "":
			path = os.Getenv("ZAKWAS_CONFIG")
		default:
			return nil, err
		}
	}
	return config.Load(path, home)
}

// Modules returns every configured module in apply order. Order matters:
// files put the mise config in place, brew installs mise, agents need the
// agent CLIs brew or mise install, and commands may need tools from any.
func Modules(cfg *config.Config, env Env) []engine.Module {
	paths := config.Paths{Home: env.Home, Root: cfg.Root}
	installer := &install.Installer{Paths: paths, StatePath: install.StatePath(env.Home), Immutable: cfg.Protect.Immutable}
	mods := []engine.Module{
		&system.Module{System: cfg.System, Paths: paths, Runner: env.Runner, PAMFile: env.PAMFile},
		&files.Module{Files: cfg.Files, Keep: templateDsts(cfg, paths), Paths: paths, Installer: installer},
		&links.Module{Links: cfg.Links, Paths: paths},
		&templates.Module{Templates: cfg.Templates, Paths: paths, Installer: installer},
	}
	if cfg.Brew != nil {
		mods = append(mods, &brew.Module{Brew: *cfg.Brew, Paths: paths, Runner: env.Runner})
	}
	if cfg.Mise != nil {
		mods = append(mods, &mise.Module{Mise: *cfg.Mise, Paths: paths, Runner: env.Runner})
	}
	if cfg.Agents != nil {
		mods = append(mods, &agents.Module{Agents: *cfg.Agents, Paths: paths, Runner: env.Runner, ClaudeDir: claudeDir(env)})
	}
	return append(mods,
		&commands.Module{Commands: cfg.Commands, Home: env.Home, Runner: env.Runner},
		&defaults.Module{Defaults: cfg.Defaults, Runner: env.Runner},
	)
}

// claudeDir is where Claude Code keeps its settings and plugin state.
func claudeDir(env Env) string {
	if dir := env.ClaudeConfigDir; dir != "" {
		return dir
	}
	return filepath.Join(env.Home, ".claude")
}

func templateDsts(cfg *config.Config, paths config.Paths) []string {
	var dsts []string
	for _, t := range cfg.Templates.Files {
		dsts = append(dsts, paths.Dst(t.Dst))
	}
	return dsts
}

func selectModules(all []engine.Module, only string) ([]engine.Module, error) {
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
			errs = append(errs, fmt.Errorf("unknown or unconfigured module %q", name))
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
