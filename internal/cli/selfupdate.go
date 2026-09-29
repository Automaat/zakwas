package cli

import (
	"context"
	"flag"
	"os"
	"strings"

	"github.com/Automaat/zakwas/internal/selfupdate"
)

func newSelfUpdateFlags(errOut *console) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("zakwas self-update", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		errOut.print("Usage: zakwas self-update [--version X.Y.Z]\n\nReplaces this zakwas binary with a release from GitHub, verified against\nthe release's checksums.txt.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	version := fs.String("version", "", "release to install (default: latest)")
	return fs, version
}

func runSelfUpdate(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, want := newSelfUpdateFlags(errOut)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 0 {
		fs.Usage()
		return ExitUsage
	}
	exe := env.Executable
	if exe == "" {
		if exe, err = selfupdate.Executable(); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}
	if msg := selfupdate.ManagedBy(exe, env.Home, os.Getenv); msg != "" {
		errOut.print("zakwas: " + msg + "\n")
		return ExitUsage
	}
	u := &selfupdate.Updater{BaseURL: env.ReleaseURL}
	var version string
	if *want == "" {
		if version, err = u.Latest(ctx); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	} else if version, err = selfupdate.NormalizeVersion(*want); err != nil {
		errOut.fail(err)
		return ExitUsage
	}
	current := strings.TrimPrefix(env.Version, "v")
	if current == version {
		out.printf("zakwas %s is already installed\n", version)
		return ExitOK
	}
	bin, err := u.Fetch(ctx, version)
	if err == nil {
		err = selfupdate.Replace(exe, bin)
	}
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	out.printf("zakwas %s → %s (%s)\n", current, version, exe)
	return ExitOK
}

// parseInterspersed accepts flags before, between and after positional
// arguments, like `zakwas init DIR --add PATH`.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
