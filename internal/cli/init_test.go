package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const (
	brewDump = "brew bundle dump --tap --formula --cask --mas --file "
	miseLs   = "mise ls --global --current --json"
	gitInit  = "git init -q -b main"
)

func initHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for name, body := range map[string]string{
		".zshrc":                     "export EDITOR=vim\n",
		".config/git/config":         "[user]\n\tname = Test\n",
		".config/git/.hidden":        "x\n",
		".config/git/.git/HEAD":      "ref\n",
		".config/mise/config.toml":   "[tools]\njq = \"latest\"\n",
		".ssh/config":                "Host *\n",
		".local/bin/tool with space": "#!/bin/sh\n",
	} {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(home, ".local/bin/tool with space"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("config", filepath.Join(home, ".config/git/link")); err != nil {
		t.Fatal(err)
	}
	return home
}

// snapshot fingerprints every path, mode and content under dir.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %s", path, info.Mode())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, " %x", sha256.Sum256(data))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInit(t *testing.T) {
	home := initHome(t)
	work := t.TempDir()
	repo := filepath.Join(work, "repo")
	brewfile := filepath.Join(repo, "Brewfile")
	fake := &dumpingFake{
		Fake: runnertest.New().
			OnOK(miseLs, `{"jq":[{"version":"1.7.1"}],"aqua:cli/cli":[{"version":"2.1.0"}],"node":[{"version":"22.1.0"},{"version":"20.3.0"},{"version":"22.1.0"}]}`).
			OnOK(brewDump+brewfile, "").
			OnOK(gitInit, ""),
		brewfile: brewfile,
		content:  "brew \"jq\"\n",
	}
	before := snapshot(t, home)

	r := invoke(Env{Home: home, Cwd: work, Runner: fake}, "", "init", "repo", "--add", "~/.zshrc", "--add", filepath.Join(home, ".config/git"), "--add", "../"+filepath.Base(home)+"/.local/bin/tool with space")
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if after := snapshot(t, home); after != before {
		t.Errorf("init changed $HOME:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	wantCfg := `# yaml-language-server: $schema=https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json

protect:
  immutable: true

files:
  - src: dotfiles/zshrc
    dst: ~/.zshrc
  - src: dotfiles/config/git
    dst: ~/.config/git
  - src: dotfiles/local/bin/tool with space
    dst: ~/.local/bin/tool with space
  - src: dotfiles/mise/config.toml
    dst: ~/.config/mise/config.toml

brew:
  file: Brewfile
  cleanup: none

mise:
  config: dotfiles/mise/config.toml
`
	if got := read(t, filepath.Join(repo, "zakwas.yaml")); got != wantCfg {
		t.Errorf("zakwas.yaml =\n%s\nwant\n%s", got, wantCfg)
	}
	wantMise := "[tools]\n\"aqua:cli/cli\" = \"2.1.0\"\nnode = [\"22.1.0\", \"20.3.0\"]\njq = \"1.7.1\"\n"
	if got := read(t, filepath.Join(repo, "dotfiles/mise/config.toml")); got != wantMise {
		t.Errorf("mise config =\n%s\nwant\n%s", got, wantMise)
	}
	for path, want := range map[string]string{
		".gitignore":                         "*.zakwas-bak*\n",
		"dotfiles/zshrc":                     "export EDITOR=vim\n",
		"dotfiles/config/git/config":         "[user]\n\tname = Test\n",
		"dotfiles/config/git/.hidden":        "x\n",
		"dotfiles/local/bin/tool with space": "#!/bin/sh\n",
	} {
		if got := read(t, filepath.Join(repo, path)); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(repo, "dotfiles/local/bin/tool with space")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("executable bit not kept: %v %v", info, err)
	}
	for _, skipped := range []string{"dotfiles/config/git/.git", "dotfiles/config/git/link"} {
		if _, err := os.Lstat(filepath.Join(repo, skipped)); err == nil {
			t.Errorf("%s copied", skipped)
		}
	}
	for _, want := range []string{"Created", "zakwas plan", "install.sh | bash -s -- --repo URL", "skipped ~/.config/git/.git (git metadata)", "skipped ~/.config/git/link (not a regular file)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stdout)
		}
	}

	for _, c := range fake.Calls {
		switch c.Name {
		case "mise":
			if c.Dir != "/" {
				t.Errorf("mise ran in %q, want /", c.Dir)
			}
		case "brew":
			if !slices.Contains(c.Env, "HOMEBREW_NO_AUTO_UPDATE=1") {
				t.Errorf("brew env %v lacks HOMEBREW_NO_AUTO_UPDATE=1", c.Env)
			}
		case "git":
			if c.Dir != repo {
				t.Errorf("git ran in %q, want %q", c.Dir, repo)
			}
		}
	}

	cfg, err := config.Load(filepath.Join(repo, "zakwas.yaml"), home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Brew == nil || cfg.Mise == nil || len(cfg.Files) != 4 || !cfg.Protect.Immutable {
		t.Errorf("loaded config = %+v", cfg)
	}
}

func TestInitWithoutBrewAndMise(t *testing.T) {
	home := initHome(t)
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := runnertest.New().Missing("brew", "mise").OnOK(gitInit, "")
	r := invoke(Env{Home: home, Cwd: home, Runner: fake}, "", "init", repo, "--add", "~/.config/mise/config.toml")
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if got := fake.Lines(); !slices.Equal(got, []string{gitInit}) {
		t.Errorf("calls = %v, want only git init", got)
	}
	want := "# yaml-language-server: $schema=" + SchemaURL + "\n\nprotect:\n  immutable: true\n\nfiles:\n  - src: dotfiles/config/mise/config.toml\n    dst: ~/.config/mise/config.toml\n"
	if got := read(t, filepath.Join(repo, "zakwas.yaml")); got != want {
		t.Errorf("zakwas.yaml =\n%s\nwant\n%s", got, want)
	}
	for _, want := range []string{"no brew section", "no mise section"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestInitTrustsDumpedTaps(t *testing.T) {
	home := initHome(t)
	repo := filepath.Join(t.TempDir(), "repo")
	brewfile := filepath.Join(repo, "Brewfile")
	fake := &dumpingFake{
		Fake:     runnertest.New().Missing("mise").OnOK(brewDump+brewfile, "").OnOK(gitInit, ""),
		brewfile: brewfile,
		content:  "tap \"acme/tools\"\nbrew \"jq\"\n",
	}
	r := invoke(Env{Home: home, Cwd: home, Runner: fake}, "", "init", repo)
	if r.code != ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if got, want := read(t, brewfile), "tap \"acme/tools\", trusted: true\nbrew \"jq\"\n"; got != want {
		t.Errorf("Brewfile = %q, want %q", got, want)
	}
	if !strings.Contains(r.stdout, "marked trusted: acme/tools") {
		t.Errorf("output does not mention the trusted tap:\n%s", r.stdout)
	}
}

// dumpingFake writes the Brewfile `brew bundle dump` would.
type dumpingFake struct {
	*runnertest.Fake
	brewfile, content string
}

func (f *dumpingFake) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name == "brew" {
		if err := os.WriteFile(f.brewfile, []byte(f.content), 0o644); err != nil {
			return runner.Result{}, err
		}
	}
	return f.Fake.Run(ctx, c)
}

func TestInitRejects(t *testing.T) {
	tests := []struct {
		name string
		args func(home, repo string) []string
		prep func(t *testing.T, home, repo string)
		msg  string
	}{
		{"no dir", func(_, _ string) []string { return nil }, nil, "Usage: zakwas init"},
		{"two dirs", func(_, repo string) []string { return []string{repo, repo + "2"} }, nil, "Usage: zakwas init"},
		{"non-empty dir", func(_, repo string) []string { return []string{repo} }, func(t *testing.T, _, repo string) {
			if err := os.MkdirAll(filepath.Join(repo, "x"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "not empty"},
		{"dir is a file", func(_, repo string) []string { return []string{repo} }, func(t *testing.T, _, repo string) {
			if err := os.WriteFile(repo, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "not a directory"},
		{"outside home", func(_, repo string) []string { return []string{repo, "--add", "/etc/hosts"} }, nil, "not inside $HOME"},
		{"home itself", func(home, repo string) []string { return []string{repo, "--add", home} }, nil, "not inside $HOME"},
		{"missing", func(_, repo string) []string { return []string{repo, "--add", "~/.nope"} }, nil, "no such file"},
		{"dot twins", func(_, repo string) []string { return []string{repo, "--add", "~/.zshrc", "--add", "~/zshrc"} }, func(t *testing.T, home, _ string) {
			if err := os.WriteFile(filepath.Join(home, "zshrc"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "collides with --add ~/.zshrc"},
		{"nested adds", func(_, repo string) []string {
			return []string{repo, "--add", "~/.config/git", "--add", "~/.config/git/config"}
		}, nil, "collides"},
		{"generated mise config", func(_, repo string) []string { return []string{repo, "--add", "~/.config/mise"} }, nil, "generates from the active mise tools"},
		{"repo inside add", func(home, _ string) []string { return []string{filepath.Join(home, ".ssh/repo"), "--add", "~/.ssh"} }, nil, "overlaps the new repo"},
		{"dir symlink", func(_, repo string) []string { return []string{repo, "--add", "~/.gitdir"} }, func(t *testing.T, home, _ string) {
			if err := os.Symlink(filepath.Join(home, ".config/git"), filepath.Join(home, ".gitdir")); err != nil {
				t.Fatal(err)
			}
		}, "symlink to a directory"},
		{"empty dir", func(_, repo string) []string { return []string{repo, "--add", "~/.empty"} }, func(t *testing.T, home, _ string) {
			if err := os.Mkdir(filepath.Join(home, ".empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "no regular files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := initHome(t)
			repo := filepath.Join(t.TempDir(), "repo")
			if tt.prep != nil {
				tt.prep(t, home, repo)
			}
			before := snapshot(t, filepath.Dir(repo))
			fake := runnertest.New()
			r := invoke(Env{Home: home, Cwd: home, Runner: fake}, "", append([]string{"init"}, tt.args(home, repo)...)...)
			if r.code != ExitUsage {
				t.Errorf("exit %d, want %d\nstderr: %s", r.code, ExitUsage, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.msg) {
				t.Errorf("stderr lacks %q:\n%s", tt.msg, r.stderr)
			}
			if len(fake.Calls) > 0 {
				t.Errorf("ran %v before rejecting", fake.Lines())
			}
			if after := snapshot(t, filepath.Dir(repo)); after != before {
				t.Errorf("rejected init wrote files:\n%s", after)
			}
		})
	}
}

func TestInitCleansUpAfterFailure(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		existed bool
	}{
		{"new dir", "repo", false},
		{"new parents", "sub/deep/repo", false},
		{"empty dir", "repo", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := initHome(t)
			parent := t.TempDir()
			repo := filepath.Join(parent, tt.repo)
			if tt.existed {
				if err := os.Mkdir(repo, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, parent)
			fake := runnertest.New().
				OnOK(miseLs, `{}`).
				On(brewDump+filepath.Join(repo, "Brewfile"), runner.Result{ExitCode: 1, Stderr: "boom"})
			r := invoke(Env{Home: home, Cwd: home, Runner: fake}, "", "init", repo, "--add", "~/.zshrc")
			if r.code != ExitErr || !strings.Contains(r.stderr, "boom") {
				t.Errorf("exit %d, stderr %q", r.code, r.stderr)
			}
			if after := snapshot(t, parent); after != before {
				t.Errorf("failed init left files:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestRepoPath(t *testing.T) {
	for rel, want := range map[string]string{
		".zshrc":                          "dotfiles/zshrc",
		".config/git/config":              "dotfiles/config/git/config",
		"Library/Application Support/foo": "dotfiles/Library/Application Support/foo",
		".config/.hidden/x":               "dotfiles/config/hidden/x",
		"..weird":                         "dotfiles/.weird",
	} {
		if got := repoPath(rel); got != want {
			t.Errorf("repoPath(%q) = %q, want %q", rel, got, want)
		}
	}
}
