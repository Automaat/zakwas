package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/Automaat/zakwas/internal/cli"
	"github.com/Automaat/zakwas/internal/runner"
)

// version is set by the release build.
var version = "dev"

// exitInterrupted follows the shell convention of 128 + SIGINT.
const exitInterrupted = 130

func main() {
	os.Exit(run())
}

// run restores default SIGINT handling after the first one, so a second
// Ctrl-C kills zakwas instead of being swallowed.
func run() int {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("zakwas", version)
		return cli.ExitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zakwas:", err)
		return cli.ExitErr
	}
	extendPath(home)
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zakwas:", err)
		return cli.ExitErr
	}
	code := cli.Main(ctx, cli.Env{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Home:    home,
		Cwd:     cwd,
		Runner:  runner.NewExec(),
		PAMFile: os.Getenv("ZAKWAS_PAM_FILE"),

		StdinTTY:      term.IsTerminal(int(os.Stdin.Fd())),
		Color:         colorOutput(),
		GitHubActions: os.Getenv("GITHUB_ACTIONS") == "true",
		Version:       version,
		Host:          hostname(),
	})
	if ctx.Err() != nil {
		return exitInterrupted
	}
	return code
}

// extendPath appends the dirs Homebrew and mise install into: this run may
// install them, and a fresh shell doesn't have them on PATH yet. exec looks
// binaries up in PATH on every call, so later modules find them.
func extendPath(home string) {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, d := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin")} {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	_ = os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

// colorOutput follows no-color.org: colors only on a terminal, never when
// NO_COLOR is set to anything.
func colorOutput() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
