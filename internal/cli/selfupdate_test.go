package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/selfupdate/selfupdatetest"
)

func TestSelfUpdate(t *testing.T) {
	base := selfupdatetest.Serve(t, "1.2.3", runtime.GOARCH, "new binary")
	tests := []struct {
		name    string
		current string
		args    []string
		want    int
		binary  string
		msg     string
	}{
		{"latest", "1.0.0", nil, ExitOK, "new binary", "zakwas 1.0.0 → 1.2.3"},
		{"from dev", "dev", nil, ExitOK, "new binary", "zakwas dev → 1.2.3"},
		{"pinned", "1.0.0", []string{"--version", "v1.2.3"}, ExitOK, "new binary", "→ 1.2.3"},
		{"already latest", "1.2.3", nil, ExitOK, "old binary", "zakwas 1.2.3 is already installed"},
		{"already pinned", "1.2.3", []string{"--version", "1.2.3"}, ExitOK, "old binary", "already installed"},
		{"missing release", "1.0.0", []string{"--version", "9.9.9"}, ExitErr, "old binary", "404"},
		{"bad version", "1.0.0", []string{"--version", "latest"}, ExitUsage, "old binary", "not a release version"},
		{"extra arg", "1.0.0", []string{"now"}, ExitUsage, "old binary", "Usage: zakwas self-update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "zakwas")
			if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			env := Env{Home: t.TempDir(), Runner: runnertest.New().Missing("gh"), Version: tt.current, Executable: exe, ReleaseURL: base}
			r := invoke(env, "", append([]string{"self-update"}, tt.args...)...)
			if r.code != tt.want {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, tt.want, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout+r.stderr, tt.msg) {
				t.Errorf("output lacks %q\nstdout: %s\nstderr: %s", tt.msg, r.stdout, r.stderr)
			}
			if got := read(t, exe); got != tt.binary {
				t.Errorf("binary = %q, want %q", got, tt.binary)
			}
		})
	}
}

func TestSelfUpdateRefusesManagedInstalls(t *testing.T) {
	home := t.TempDir()
	caskroom := t.TempDir()
	t.Setenv("MISE_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOMEBREW_PREFIX", caskroom)
	tests := []struct {
		name, exe, msg string
	}{
		{"mise", filepath.Join(home, ".local/share/mise/installs/github-automaat-zakwas/1.0.0/zakwas"), `bump "github:Automaat/zakwas" in your mise config`},
		{"homebrew cask", filepath.Join(caskroom, "Caskroom/zakwas/1.0.0/zakwas"), "brew upgrade zakwas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.MkdirAll(filepath.Dir(tt.exe), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tt.exe, []byte("old binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			env := Env{Home: home, Runner: runnertest.New(), Version: "1.0.0", Executable: tt.exe, ReleaseURL: "http://127.0.0.1:1/unreachable"}
			r := invoke(env, "", "self-update")
			if r.code != ExitUsage || !strings.Contains(r.stderr, tt.msg) {
				t.Errorf("exit %d, stderr %q; want %d mentioning %q", r.code, r.stderr, ExitUsage, tt.msg)
			}
			if got := read(t, tt.exe); got != "old binary" {
				t.Errorf("binary replaced: %q", got)
			}
		})
	}
}

// ghFake answers the gh calls self-update makes; the archive path in the
// verify call is a temp file, so it can't be scripted with runnertest.
type ghFake struct {
	installed  bool
	loggedIn   bool
	verifyExit int
	verified   []string
}

func (g *ghFake) Installed(name string) bool { return name != "gh" || g.installed }

func (g *ghFake) Run(_ context.Context, c runner.Cmd) (runner.Result, error) {
	line := c.String()
	switch {
	case line == "gh auth status":
		if !g.loggedIn {
			return runner.Result{ExitCode: 1}, nil
		}
		return runner.Result{}, nil
	case line == "gh attestation verify --help":
		return runner.Result{Stdout: "--signer-workflow string\n--source-ref string\n"}, nil
	case strings.HasPrefix(line, "gh attestation verify "):
		if _, err := os.Stat(c.Args[2]); err != nil {
			return runner.Result{}, err
		}
		g.verified = append(g.verified, strings.Join(c.Args[3:], " "))
		return runner.Result{ExitCode: g.verifyExit, Stderr: "no attestation"}, nil
	}
	return runner.Result{}, fmt.Errorf("unexpected %q", line)
}

func TestSelfUpdateAttestation(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		gh       ghFake
		want     int
		binary   string
		msg      string
		verified bool
	}{
		{"verified", "1.2.3", ghFake{installed: true, loggedIn: true}, ExitOK, "new binary", "Verified build provenance of zakwas_1.2.3_darwin_", true},
		{"verification fails", "1.2.3", ghFake{installed: true, loggedIn: true, verifyExit: 1}, ExitErr, "old binary", "attestation verification failed", true},
		{"gh missing", "1.2.3", ghFake{}, ExitOK, "new binary", "Skipped attestation check", false},
		{"gh logged out", "1.2.3", ghFake{installed: true}, ExitOK, "new binary", "--source-ref refs/tags/v1.2.3", false},
		{"predates attestations", "0.3.9", ghFake{installed: true, loggedIn: true}, ExitOK, "new binary", "predates build attestations", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := selfupdatetest.Serve(t, tt.version, runtime.GOARCH, "new binary")
			exe := filepath.Join(t.TempDir(), "zakwas")
			if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			gh := tt.gh
			env := Env{Home: t.TempDir(), Runner: &gh, Version: "0.1.0", Executable: exe, ReleaseURL: base}
			r := invoke(env, "", "self-update", "--version", tt.version)
			if r.code != tt.want || !strings.Contains(r.stdout+r.stderr, tt.msg) {
				t.Errorf("exit %d, want %d mentioning %q\nstdout: %s\nstderr: %s", r.code, tt.want, tt.msg, r.stdout, r.stderr)
			}
			if got := read(t, exe); got != tt.binary {
				t.Errorf("binary = %q, want %q", got, tt.binary)
			}
			wantArgs := "--repo Automaat/zakwas --signer-workflow Automaat/zakwas/.github/workflows/release.yml --source-ref refs/tags/v" + tt.version
			if tt.verified != (len(gh.verified) == 1) || (tt.verified && gh.verified[0] != wantArgs) {
				t.Errorf("verify calls = %q, want one with %q: %v", gh.verified, wantArgs, tt.verified)
			}
		})
	}
}
