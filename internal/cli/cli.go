// Package cli wires config, modules and the engine behind the zakwas commands.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/modules/brew"
	"github.com/Automaat/zakwas/internal/modules/commands"
	"github.com/Automaat/zakwas/internal/modules/defaults"
	"github.com/Automaat/zakwas/internal/modules/files"
	"github.com/Automaat/zakwas/internal/modules/links"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/modules/system"
	"github.com/Automaat/zakwas/internal/modules/templates"
	"github.com/Automaat/zakwas/internal/runner"
)

const usage = `zakwas converges this Mac to zakwas.yaml.

Usage:
  zakwas [flags] <command>

Commands:
  plan     show pending changes
  apply    show pending changes, confirm, apply them
  upgrade  refresh Homebrew's package list, then apply (picks up new brew versions)
  check    exit 2 when anything drifted (for CI/cron)
  version  print the zakwas version

Flags:
`

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
	fs := flag.NewFlagSet("zakwas", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		errOut.print(usage)
		fs.PrintDefaults()
	}
	cfgPath := fs.String("c", "", "path to zakwas.yaml (default: search upward from cwd, then $ZAKWAS_CONFIG)")
	only := fs.String("only", "", "comma-separated modules to run (default: all)")
	yes := fs.Bool("y", false, "apply without asking for confirmation")
	diff := fs.Bool("diff", false, "show the content diff of every file change")
	cmd, ok := parseArgs(fs, env.Args)
	if !ok {
		fs.Usage()
		return ExitUsage
	}
	if !slices.Contains([]string{"plan", "apply", "upgrade", "check"}, cmd) {
		errOut.printf("unknown command %q\n", cmd)
		fs.Usage()
		return ExitUsage
	}

	cfg, err := loadConfig(*cfgPath, env.Cwd, env.Home)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	mods, err := selectModules(Modules(cfg, env), *only)
	if err != nil {
		errOut.fail(err)
		return ExitUsage
	}

	if cmd == "upgrade" {
		if cfg.Brew == nil {
			errOut.print("zakwas: upgrade needs a brew section in zakwas.yaml\n")
			return ExitUsage
		}
		refresh := runner.Cmd{Name: "brew", Args: []string{"update", "--quiet"}, Stream: true}
		if err := runner.Check(ctx, env.Runner, refresh); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}

	plan := engine.Build(ctx, mods)
	if err := engine.Print(out, plan, *diff); err != nil {
		return ExitErr
	}
	planErr := plan.Err()

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
		if planErr != nil {
			errOut.fail(planErr)
			return ExitErr
		}
		out.print("nothing to do\n")
		return ExitOK
	}
	for _, w := range repoWarnings(ctx, env.Runner, cfg.Root) {
		errOut.print("zakwas: warning: " + w + "\n")
	}
	if !*yes && !confirm(env.Stdin, out, plan.Count()) {
		out.print("aborted\n")
		return ExitErr
	}
	applyErr := engine.Apply(ctx, func(s string) { out.print(s + "\n") }, plan)
	if err := recordHistory(ctx, env, cfg.Root, plan, errors.Join(planErr, applyErr)); err != nil {
		errOut.print("zakwas: warning: history not recorded: " + err.Error() + "\n")
	}
	if err := errors.Join(planErr, applyErr); err != nil {
		errOut.fail(err)
		return ExitErr
	}
	out.print("done\n")
	return ExitOK
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
func parseArgs(fs *flag.FlagSet, args []string) (string, bool) {
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return "", false
	}
	cmd := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() != 0 {
		return "", false
	}
	return cmd, true
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
// files put the mise config in place, brew installs mise, and commands may
// need tools from either.
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
	return append(mods,
		&commands.Module{Commands: cfg.Commands, Home: env.Home, Runner: env.Runner},
		&defaults.Module{Defaults: cfg.Defaults, Runner: env.Runner},
	)
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
	out.printf("\napply %d change(s)? [y/N] ", n)
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
