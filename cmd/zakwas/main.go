package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/Automaat/zakwas/internal/cli"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/selfupdate"
)

// version is set by the release build.
var version = "dev"

// goosOverride is set by tests to run the CLI as another operating system;
// empty means the real one.
var goosOverride string

func main() {
	os.Exit(run())
}

// run stops on the first of SIGINT, SIGTERM and SIGHUP and exits 128 plus its
// number; a second signal kills zakwas.
func run() int {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("zakwas", version)
		return cli.ExitOK
	}
	ctx, stop, caught := watchSignals(context.Background(), dieOf, stopSignals(os.Args[1:], signal.Ignored)...)
	defer stop()

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zakwas:", err)
		return cli.ExitErr
	}
	extendPath(home)
	if runtime.GOOS == "windows" {
		selfupdate.CleanupOld(home)
	}
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
		UID:     os.Getuid(),
		Runner:  runner.NewExec(),
		PAMFile: os.Getenv("ZAKWAS_PAM_FILE"),

		ClaudeConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"),
		CodexHome:       os.Getenv("CODEX_HOME"),
		CopilotHome:     os.Getenv("COPILOT_HOME"),
		GeminiHome:      os.Getenv("GEMINI_CLI_HOME"),
		LockHeldBy:      os.Getenv(cli.LockHeldEnv),
		AgentSession:    cli.DetectAgentSession(os.Getenv),

		StdinTTY:      term.IsTerminal(int(os.Stdin.Fd())),
		Color:         colorOutput(),
		GitHubActions: os.Getenv("GITHUB_ACTIONS") == "true",
		Version:       version,
		Host:          hostname(),
		GOOS:          goosOverride,
	})
	if sig := caught(); sig != nil {
		return exitCodeFor(sig)
	}
	return code
}

// extendPath appends the dirs zakwas's own bootstraps install into: this run
// may install them, and a fresh shell doesn't have them on PATH yet. exec
// looks binaries up in PATH on every call, so later modules find them. The
// dirs need not exist yet, for the same reason.
func extendPath(home string) {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	for _, d := range extraPathDirs(runtime.GOOS, home) {
		if !slices.ContainsFunc(dirs, func(have string) bool { return samePathEntry(have, d, runtime.GOOS) }) {
			dirs = append(dirs, d)
		}
	}
	_ = os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

// extraPathDirs are Homebrew's prefix on macOS, the dir both OSes put
// user-installed tools in (not on Windows), and ~/.local/bin, where mise and the agent CLIs'
// own installers put binaries.
func extraPathDirs(goos, home string) []string {
	userBin := filepath.Join(home, ".local", "bin")
	if goos == "windows" {
		return []string{userBin}
	}
	dirs := []string{"/usr/local/bin", userBin}
	if goos == platform.Darwin {
		dirs = append([]string{"/opt/homebrew/bin"}, dirs...)
	}
	return dirs
}

func samePathEntry(a, b, goos string) bool {
	if goos == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return a == b
}

// colorOutput follows no-color.org: colors only on a terminal, never when
// NO_COLOR is set to anything.
func colorOutput() bool {
	if _, set := os.LookupEnv("NO_COLOR"); set || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd())) && enableANSI(os.Stdout) && enableANSI(os.Stderr)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
