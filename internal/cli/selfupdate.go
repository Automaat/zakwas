package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
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
	bin, err := fetchVerified(ctx, env, u, version, out)
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

// fetchVerified downloads a release, checks its SHA256 and, like install.sh,
// its build provenance when an authenticated, recent enough gh is present.
func fetchVerified(ctx context.Context, env Env, u *selfupdate.Updater, version string, out *console) ([]byte, error) {
	archive, err := u.Download(ctx, version)
	if err != nil {
		return nil, err
	}
	asset := u.Asset(version)
	switch {
	case !selfupdate.Attested(version):
		out.printf("zakwas %s predates build attestations; skipping provenance check\n", version)
	case !selfupdate.CanVerify(ctx, env.Runner):
		out.printf("Skipped attestation check (needs a recent gh, logged in); to verify: gh release download v%s -R Automaat/zakwas -p %s && gh attestation verify %s %s\n",
			version, asset, asset, strings.Join(selfupdate.AttestationArgs(version), " "))
	default:
		dir, err := os.MkdirTemp("", "zakwas-update-")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		file := filepath.Join(dir, asset)
		if err := os.WriteFile(file, archive, 0o600); err != nil {
			return nil, err
		}
		if err := selfupdate.VerifyAttestation(ctx, env.Runner, file, version); err != nil {
			return nil, err
		}
		out.printf("Verified build provenance of %s\n", asset)
	}
	return selfupdate.Extract(archive)
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
