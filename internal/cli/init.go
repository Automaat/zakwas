package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/modules/brew"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/runner"
)

// SchemaURL is the JSON Schema init points editors at.
const SchemaURL = "https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json"

const (
	dotfilesDir = "dotfiles"
	miseSrc     = "dotfiles/mise/config.toml"
)

var (
	bareKey   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

func newInitFlags(errOut *console) (*flag.FlagSet, *[]string) {
	var adds []string
	fs := flag.NewFlagSet("zakwas init", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		errOut.print(`Usage: zakwas init DIR [--add PATH]...

Creates a starter config repo in DIR (new or empty) from this Mac: a
Brewfile (taps, formulae, casks, mas apps) from brew bundle dump, your
global mise config with every tool pinned to its exact version, and every --add file or directory copied under
dotfiles/. Nothing outside DIR is changed.

Flags:
`)
		fs.PrintDefaults()
	}
	fs.Func("add", "file or directory under $HOME to copy into the repo and manage (repeatable)", func(s string) error {
		adds = append(adds, s)
		return nil
	})
	return fs, &adds
}

// addition is one --add path: its files are copied from the machine to
// <repo>/<src>, and zakwas.yaml maps src back to dst.
type addition struct {
	src, dst string
	files    []copyFile
}

type copyFile struct {
	from, to string
	perm     fs.FileMode
}

type initPlan struct {
	dir     string
	home    string
	adds    []addition
	skipped []string
	brew    bool
	mise    bool
	orphans int
	trusted []string
}

func runInit(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, adds := newInitFlags(errOut)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fs.Usage()
		return ExitUsage
	}
	p := &initPlan{
		dir:  absFrom(env.Cwd, env.Home, pos[0]),
		home: env.Home,
		brew: env.Runner.Installed("brew"),
		mise: env.Runner.Installed("mise"),
	}
	if err := p.check(*adds, env.Cwd); err != nil {
		errOut.fail(err)
		return ExitUsage
	}
	p.orphans = p.countOrphans()
	var miseToml []byte
	if p.mise {
		if miseToml, err = globalMiseConfig(ctx, env.Runner, env.Home); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}
	created, err := p.write(ctx, env.Runner, miseToml)
	if err != nil {
		errOut.fail(errors.Join(err, cleanup(p.dir, created)))
		return ExitErr
	}
	out.print(p.summary())
	return ExitOK
}

// countOrphans counts files zakwas installed from another config: plan in
// the new repo would list them as no longer managed, and apply delete them.
func (p *initPlan) countOrphans() int {
	state, err := install.LoadState(install.StatePath(p.home))
	if err != nil {
		return 0
	}
	wanted := map[string]bool{config.Paths{Home: p.home}.Dst(mise.InstalledConfig): p.mise}
	for _, a := range p.adds {
		for _, f := range a.files {
			wanted[f.from] = true
		}
	}
	n := 0
	for _, dst := range state.Keys() {
		if !wanted[dst] {
			n++
		}
	}
	return n
}

// absFrom resolves a command-line path the way a shell user expects: "~/"
// against home, relative paths against cwd.
func absFrom(cwd, home, path string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(cwd, path)
}

// check validates everything before init writes a single file, so a bad
// argument never leaves a half-made repo behind.
func (p *initPlan) check(adds []string, cwd string) error {
	if info, err := os.Stat(p.dir); err == nil && !info.IsDir() {
		return fmt.Errorf("%s exists and is not a directory", p.dir)
	}
	switch entries, err := os.ReadDir(p.dir); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%s already exists and is not empty; pick a new or empty directory", p.dir)
	}
	claimed := map[string]string{}
	if p.mise {
		claimed[strings.ToLower(miseSrc)] = "the mise config"
	}
	for _, arg := range adds {
		a, err := p.addition(absFrom(cwd, p.home, arg))
		if err != nil {
			return fmt.Errorf("--add %s: %w", arg, err)
		}
		key := strings.ToLower(a.src)
		for other, what := range claimed {
			if overlapsPath(key, other) {
				return fmt.Errorf("--add %s: repo path %s collides with %s", arg, a.src, what)
			}
		}
		claimed[key] = "--add " + arg
		if p.mise && overlapsPath(strings.ToLower(a.dst), strings.ToLower(mise.InstalledConfig)) {
			return fmt.Errorf("--add %s: overlaps %s, which init generates from the active mise tools", arg, mise.InstalledConfig)
		}
		p.adds = append(p.adds, a)
	}
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(p.config()), &cfg); err != nil {
		return fmt.Errorf("generated %s: %w", config.FileName, err)
	}
	cfg.Root = p.dir
	return cfg.Validate(p.home)
}

func overlapsPath(a, b string) bool {
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

func (p *initPlan) addition(path string) (addition, error) {
	rel, err := filepath.Rel(p.home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return addition{}, fmt.Errorf("%s is not inside $HOME (%s)", path, p.home)
	}
	if overlapsPath(p.dir, path) {
		return addition{}, fmt.Errorf("%s overlaps the new repo %s", path, p.dir)
	}
	a := addition{src: repoPath(rel), dst: "~/" + filepath.ToSlash(rel)}
	info, err := os.Stat(path)
	if err != nil {
		return addition{}, err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return addition{}, errors.New("not a regular file or directory")
		}
		a.files = []copyFile{{from: path, to: a.src, perm: info.Mode().Perm()}}
		return a, nil
	}
	if l, err := os.Lstat(path); err == nil && l.Mode()&fs.ModeSymlink != 0 {
		return addition{}, errors.New("is a symlink to a directory, and zakwas won't write through it; add the directory it points to, or replace the link")
	}
	err = filepath.WalkDir(path, func(file string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			p.skipped = append(p.skipped, file+" (git metadata)")
			return filepath.SkipDir
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			p.skipped = append(p.skipped, file+" (not a regular file)")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		inner, err := filepath.Rel(path, file)
		if err != nil {
			return err
		}
		a.files = append(a.files, copyFile{from: file, to: a.src + "/" + filepath.ToSlash(inner), perm: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return addition{}, err
	}
	if len(a.files) == 0 {
		return addition{}, errors.New("directory has no regular files to copy")
	}
	return a, nil
}

// repoPath maps a home-relative path to its place in the repo: the same
// path under dotfiles/, each component without its leading dot, so
// ~/.config/git/config lands in dotfiles/config/git/config and nothing in
// the repo is hidden. Distinct home paths map to distinct repo paths except
// dot/no-dot twins (~/.foo and ~/foo), which check rejects.
func repoPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if trimmed := strings.TrimPrefix(part, "."); trimmed != "" {
			parts[i] = trimmed
		}
	}
	return dotfilesDir + "/" + strings.Join(parts, "/")
}

func (p *initPlan) config() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# yaml-language-server: $schema=%s\n\n", SchemaURL)
	b.WriteString("protect:\n  immutable: true\n")
	if len(p.adds) > 0 || p.mise {
		b.WriteString("\nfiles:\n")
		for _, a := range p.adds {
			fmt.Fprintf(&b, "  - src: %s\n    dst: %s\n", yamlString(a.src), yamlString(a.dst))
		}
		if p.mise {
			fmt.Fprintf(&b, "  - src: %s\n    dst: %s\n", miseSrc, mise.InstalledConfig)
		}
	}
	if p.brew {
		b.WriteString("\nbrew:\n  file: Brewfile\n  cleanup: none\n")
	}
	if p.mise {
		fmt.Fprintf(&b, "\nmise:\n  config: %s\n", miseSrc)
	}
	return b.String()
}

func yamlString(s string) string {
	data, err := yaml.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return strings.TrimSuffix(string(data), "\n")
}

// write creates the repo and returns the topmost directory it created, if
// any, so a failed init can remove everything it made.
func (p *initPlan) write(ctx context.Context, r runner.Runner, miseToml []byte) (string, error) {
	created := topMissing(p.dir)
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return created, err
	}
	for _, a := range p.adds {
		for _, f := range a.files {
			if err := copyInto(p.dir, f); err != nil {
				return created, err
			}
		}
	}
	if p.mise {
		if err := writeFile(filepath.Join(p.dir, miseSrc), miseToml, 0o644); err != nil {
			return created, err
		}
	}
	if p.brew {
		dump := runner.Cmd{
			Name: "brew",
			Args: []string{"bundle", "dump", "--tap", "--formula", "--cask", "--mas", "--file", filepath.Join(p.dir, "Brewfile")},
			Env:  []string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1"},
		}
		if err := runner.Check(ctx, r, dump); err != nil {
			return created, err
		}
		if err := p.trustTaps(); err != nil {
			return created, err
		}
	}
	cfgPath := filepath.Join(p.dir, config.FileName)
	if err := writeFile(cfgPath, []byte(p.config()), 0o644); err != nil {
		return created, err
	}
	if err := writeFile(filepath.Join(p.dir, ".gitignore"), []byte("*.zakwas-bak*\n"), 0o644); err != nil {
		return created, err
	}
	if _, err := config.Load(cfgPath, p.home); err != nil {
		return created, err
	}
	return created, runner.Check(ctx, r, runner.Cmd{Name: "git", Args: []string{"init", "-q", "-b", "main"}, Dir: p.dir})
}

// trustTaps adds `trusted: true` to taps this Mac already has, which the
// brew module requires of every third-party tap.
func (p *initPlan) trustTaps() error {
	file := filepath.Join(p.dir, "Brewfile")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	data, p.trusted = brew.TrustTaps(data)
	if len(p.trusted) == 0 {
		return nil
	}
	return writeFile(file, data, 0o644)
}

// copyInto keeps the source's permissions but makes the copy owner-writable:
// installed copies are read-only, and the repo copy is the one to edit.
func copyInto(dir string, f copyFile) error {
	data, err := os.ReadFile(f.from)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, filepath.FromSlash(f.to)), data, f.perm|0o600)
}

func writeFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

// topMissing returns the highest ancestor of dir (or dir itself) that does
// not exist yet, or "" when dir exists.
func topMissing(dir string) string {
	top := ""
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || filepath.Dir(d) == d {
			return top
		}
		top = d
	}
}

// cleanup undoes a failed init: created is the topmost directory init made;
// otherwise dir was empty, so everything in it is init's.
func cleanup(dir, created string) error {
	if created != "" {
		return os.RemoveAll(created)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}

func (p *initPlan) summary() string {
	paths := config.Paths{Home: p.home}
	dir := paths.Pretty(p.dir)
	var b strings.Builder
	fmt.Fprintf(&b, "Created %s\n  %s\n  .gitignore\n", dir, config.FileName)
	if p.brew {
		fmt.Fprintf(&b, "  %-29s taps, formulae, casks, mas apps from brew bundle dump\n", "Brewfile")
		if len(p.trusted) > 0 {
			fmt.Fprintf(&b, "  %-29s marked trusted: %s (already tapped here)\n", "", strings.Join(p.trusted, ", "))
		}
	} else {
		b.WriteString("  no brew section: Homebrew is not installed\n")
	}
	if p.mise {
		fmt.Fprintf(&b, "  %-29s global mise config, active tools pinned to exact versions\n", miseSrc)
	} else {
		b.WriteString("  no mise section: mise is not installed\n")
	}
	for _, a := range p.adds {
		fmt.Fprintf(&b, "  %-29s ← %s\n", a.src, a.dst)
	}
	for _, s := range p.skipped {
		fmt.Fprintf(&b, "  skipped %s\n", paths.Pretty(s))
	}
	if p.orphans > 0 {
		fmt.Fprintf(&b, "\nWarning: zakwas already manages %d other file(s) on this Mac from another config;\n`zakwas plan` in the new repo lists them as no longer managed, and apply deletes them.\n", p.orphans)
	}
	fmt.Fprintf(&b, `
Next steps:
  1. cd %s && zakwas plan
     Files that already match are adopted; others are backed up to *.zakwas-bak on apply.
  2. Create an empty GitHub repo, then:
     git add -A && git commit -m "Initial config" && git remote add origin URL && git push -u origin main
  3. On a new Mac:
     curl -fsSL https://raw.githubusercontent.com/Automaat/zakwas/main/install.sh | bash -s -- --repo URL
`, shellQuote(dir))
	return b.String()
}

func shellQuote(s string) string {
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return "~/" + shellQuote(rest)
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
