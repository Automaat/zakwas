// Package brew converges Homebrew to a Brewfile via `brew bundle`.
package brew

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// brew never self-updates: planning must be read-only and fast, and `brew
// bundle` auto-updating at apply time would upgrade beyond what was planned.
// `zakwas upgrade` runs `brew update` explicitly instead.
var noAutoUpdate = []string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1"}

type Module struct {
	Brew   config.Brew
	Paths  config.Paths
	Runner runner.Runner
}

func (m *Module) Name() string { return "brew" }

func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	files, err := m.read()
	if err != nil {
		return nil, err
	}
	entries := files.entries()
	src := files.source()
	if !m.Runner.Installed("brew") {
		return m.planBootstrap(src, files)
	}

	if err := files.requireTrust(); err != nil {
		return nil, err
	}

	// The read-only queries are independent and each costs a brew start-up
	// of about a second, so run them together. Errors are reported in
	// query order, as sequential planning would.
	var (
		trust, cleanup        []engine.Change
		missing               []string
		outdated              []Outdated
		trustErr, missingErr  error
		outdatedErr, cleanErr error
		wg                    sync.WaitGroup
	)
	wg.Go(func() { trust, trustErr = m.planTrust(ctx, entries) })
	wg.Go(func() { missing, missingErr = m.missing(ctx, src) })
	if m.Brew.Upgrade {
		wg.Go(func() { outdated, outdatedErr = m.outdated(ctx, entries) })
	}
	wg.Go(func() { cleanup, cleanErr = m.planCleanup(ctx, src) })
	wg.Wait()
	if err := cmp.Or(trustErr, missingErr, outdatedErr, cleanErr); err != nil {
		return nil, err
	}

	var install []engine.Change
	for _, e := range missing {
		install = append(install, engine.Change{Action: engine.Create, Target: e})
	}
	for _, e := range outdated {
		install = append(install, engine.Change{Action: engine.Update, Target: e.Name, From: e.From, To: e.To})
	}
	if len(install) > 0 {
		install = append(install, m.bundleInstall(src))
	}
	return append(append(trust, install...), cleanup...), nil
}

// bundleFile is one Brewfile the module converges: the main config's, or the
// one of a layer (layer is its name).
type bundleFile struct {
	path    string
	layer   string
	data    []byte
	entries []Entry
}

// label names the file in errors: the Brewfile, or a layer's Brewfile.
func (f bundleFile) label() string {
	if f.layer == "" {
		return "the Brewfile"
	}
	return fmt.Sprintf("the Brewfile of layer %q (%s)", f.layer, f.path)
}

type bundleFiles []bundleFile

// read loads the main Brewfile and then each layer's, in registration order.
// A layer's Brewfile that cannot be read fails the plan naming the layer.
func (m *Module) read() (bundleFiles, error) {
	fs := make(bundleFiles, 0, 1+len(m.Brew.Layers))
	add := func(path, layer string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			if layer != "" {
				return fmt.Errorf("layer %q: %w", layer, err)
			}
			return err
		}
		fs = append(fs, bundleFile{path: path, layer: layer, data: data, entries: ParseEntries(data)})
		return nil
	}
	if err := add(m.Paths.Src(m.Brew.File), ""); err != nil {
		return nil, err
	}
	for _, l := range m.Brew.Layers {
		if err := add(l.File, l.Layer); err != nil {
			return nil, err
		}
	}
	return fs, fs.requireWholeFiles()
}

var endsEarly = regexp.MustCompile(`(?m)^\s*(__END__|return)\b`)

// requireWholeFiles rejects a Brewfile that stops brew reading early. With
// layers the files are one Ruby program, so a top-level `__END__` or `return`
// would hide every later Brewfile from brew while zakwas still counts its
// entries, and cleanup would remove what the layer lists.
func (fs bundleFiles) requireWholeFiles() error {
	if len(fs) < 2 {
		return nil
	}
	var errs []error
	for _, f := range fs {
		if g := endsEarly.FindSubmatch(f.data); g != nil {
			errs = append(errs, fmt.Errorf("%s has a top-level `%s`; with layers brew would stop reading there and ignore the Brewfiles after it", f.label(), g[1]))
		}
	}
	return errors.Join(errs...)
}

// entries lists the tap/brew/cask entries of every Brewfile, in order.
func (fs bundleFiles) entries() []Entry {
	var out []Entry
	for _, f := range fs {
		out = append(out, f.entries...)
	}
	return out
}

// source is how brew bundle is pointed at the Brewfiles: the file itself, or
// with layers their concatenation on stdin (`--file=-`), so brew sees one
// Brewfile and cleanup keeps what any of them lists. Nothing is written to
// disk, so plan and check stay read-only. A package listed in several files
// is harmless to brew bundle.
type source struct {
	file  string
	piped bool
	stdin string
}

func (fs bundleFiles) source() source {
	if len(fs) == 1 {
		return source{file: fs[0].path}
	}
	var b strings.Builder
	for _, f := range fs {
		b.Write(f.data)
		if !bytes.HasSuffix(f.data, []byte("\n")) {
			b.WriteString("\n")
		}
	}
	return source{piped: true, stdin: b.String()}
}

// cmd builds a brew bundle command: sub, the Brewfile flag, then rest.
func (s source) cmd(sub, rest []string, stream bool) runner.Cmd {
	flag := []string{"--file", s.file}
	if s.piped {
		flag = []string{"--file=-"}
	}
	args := slices.Concat(sub, flag, rest)
	return runner.Cmd{Name: "brew", Args: args, Env: noAutoUpdate, Stdin: s.stdin, Stream: stream}
}

// installerCommit pins the Homebrew installer script, so a compromised or
// broken upstream HEAD can't reach machines until Renovate proposes it.
// renovate: datasource=git-refs depName=https://github.com/Homebrew/install branch=main
const installerCommit = "85d1a3fc80d8a63932f632aca8f787de11d14a64"

// homebrewInstall primes sudo while a terminal is attached, so the official
// installer can run NONINTERACTIVE (no "press RETURN") with cached credentials.
const homebrewInstall = `sudo -v && NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/` +
	installerCommit + `/install.sh)"`

// planBootstrap installs Homebrew, then everything in the Brewfile; taps are
// trusted by `brew bundle install` from their `trusted:` options. A fresh
// Homebrew has nothing to clean up.
func (m *Module) planBootstrap(src source, files bundleFiles) ([]engine.Change, error) {
	if err := files.requireTrust(); err != nil {
		return nil, err
	}
	cmd := runner.Cmd{Name: "/bin/bash", Args: []string{"-c", homebrewInstall}, Stream: true}
	changes := []engine.Change{{
		Action: engine.Create, Target: "Homebrew", Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}}
	seen := map[string]bool{}
	for _, e := range files.entries() {
		target := e.Kind + " " + e.Name
		if !seen[target] {
			seen[target] = true
			changes = append(changes, engine.Change{Action: engine.Create, Target: target})
		}
	}
	return append(changes, m.bundleInstall(src)), nil
}

type trustJSON struct {
	Taps []string `json:"taps"`
}

func thirdParty(e Entry) bool {
	return e.Kind == "tap" && !strings.HasPrefix(e.Name, "homebrew/")
}

// requireTrust makes the Brewfiles the only source of tap trust: `brew bundle
// cleanup --force` resets Homebrew's trust store to the Brewfiles' `trusted:`
// options, so trust granted any other way is wiped on the next cleanup and
// installs from the tap start failing. Each Brewfile answers for its own taps,
// and the error names a layer's Brewfile.
func (fs bundleFiles) requireTrust() error {
	var errs []error
	for _, f := range fs {
		for _, e := range f.entries {
			if thirdParty(e) && !e.Trusted {
				errs = append(errs, fmt.Errorf("tap %q in %s needs `trusted: true`", e.Name, f.label()))
			}
		}
	}
	return errors.Join(errs...)
}

// planTrust trusts the Brewfile's trusted taps up front: `brew bundle
// install` would too, but only runs when something is missing, and Homebrew
// refuses to load formulae from untrusted third-party taps.
func (m *Module) planTrust(ctx context.Context, entries []Entry) ([]engine.Change, error) {
	var taps []string
	for _, e := range entries {
		if thirdParty(e) && !slices.Contains(taps, e.Name) {
			taps = append(taps, e.Name)
		}
	}
	if len(taps) == 0 {
		return nil, nil
	}
	out, err := runner.Output(ctx, m.Runner, runner.Cmd{Name: "brew", Args: []string{"trust", "--json=v1"}, Env: noAutoUpdate})
	if err != nil {
		return nil, err
	}
	var trusted trustJSON
	if err := json.Unmarshal([]byte(out), &trusted); err != nil {
		return nil, fmt.Errorf("brew trust: %w", err)
	}
	var changes []engine.Change
	for _, tap := range taps {
		if slices.Contains(trusted.Taps, tap) {
			continue
		}
		cmd := runner.Cmd{Name: "brew", Args: []string{"trust", "--tap", tap}, Env: noAutoUpdate}
		changes = append(changes, engine.Change{
			Action: engine.Create, Target: "trust tap " + tap,
			Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
		})
	}
	return changes, nil
}

func (m *Module) bundleInstall(src source) engine.Change {
	var rest []string
	if !m.Brew.Upgrade {
		rest = append(rest, "--no-upgrade")
	}
	cmd := src.cmd([]string{"bundle", "install"}, rest, true)
	return engine.Change{
		Action: engine.Run, Target: "brew bundle install", Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}
}

var missingLine = regexp.MustCompile(`^→ (\S+) (\S+) needs to be `)

func (m *Module) missing(ctx context.Context, src source) ([]string, error) {
	cmd := src.cmd([]string{"bundle", "check"}, []string{"--verbose", "--no-upgrade"}, false)
	res, err := m.Runner.Run(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 0 {
		return nil, nil
	}
	var out []string
	for line := range strings.Lines(res.Stdout + res.Stderr) {
		if g := missingLine.FindStringSubmatch(strings.TrimSpace(line)); g != nil {
			// A package listed in two Brewfiles is reported by brew twice.
			if entry := strings.ToLower(g[1]) + " " + g[2]; !slices.Contains(out, entry) {
				out = append(out, entry)
			}
		}
	}
	if len(out) == 0 {
		return nil, res.Err(cmd)
	}
	return out, nil
}

type outdatedEntry struct {
	Name              string   `json:"name"`
	Pinned            bool     `json:"pinned"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
}

// Outdated is a Brewfile entry with a newer version available.
type Outdated struct {
	Name, From, To string
}

type outdatedJSON struct {
	Formulae []outdatedEntry `json:"formulae"`
	Casks    []outdatedEntry `json:"casks"`
}

// outdated skips pinned entries: brew bundle doesn't upgrade them, so they
// would be drift that never converges.
func (m *Module) outdated(ctx context.Context, entries []Entry) ([]Outdated, error) {
	out, err := runner.Output(ctx, m.Runner, runner.Cmd{Name: "brew", Args: []string{"outdated", "--json=v2"}, Env: noAutoUpdate})
	if err != nil {
		return nil, err
	}
	var parsed outdatedJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("brew outdated: %w", err)
	}
	wanted := map[string]bool{}
	for _, e := range entries {
		wanted[e.Kind+" "+path.Base(e.Name)] = true
	}
	var res []Outdated
	add := func(kind string, list []outdatedEntry) {
		for _, e := range list {
			if key := kind + " " + path.Base(e.Name); wanted[key] && !e.Pinned {
				res = append(res, Outdated{Name: key, From: strings.Join(e.InstalledVersions, ", "), To: e.CurrentVersion})
			}
		}
	}
	add("brew", parsed.Formulae)
	add("cask", parsed.Casks)
	return res, nil
}

func (m *Module) planCleanup(ctx context.Context, src source) ([]engine.Change, error) {
	mode := m.Brew.Cleanup
	if mode == "" || mode == config.CleanupNone {
		return nil, nil
	}
	dryRun := src.cmd([]string{"bundle", "cleanup"}, nil, false)
	res, err := m.Runner.Run(ctx, dryRun)
	if err != nil {
		return nil, err
	}
	removals := ParseCleanup(res.Stdout)
	if res.ExitCode != 0 && len(removals) == 0 {
		return nil, res.Err(dryRun)
	}
	if len(removals) == 0 {
		return nil, nil
	}
	var changes []engine.Change
	for _, r := range removals {
		changes = append(changes, engine.Change{Action: engine.Remove, Target: r, Destructive: true})
	}
	var rest []string
	if mode == config.CleanupZap {
		rest = append(rest, "--zap")
	}
	cmd := src.cmd([]string{"bundle", "cleanup", "--force"}, rest, true)
	return append(changes, engine.Change{
		Action: engine.Run, Target: "brew bundle cleanup",
		Detail: mode, Destructive: true, Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}), nil
}

var uninstallHeading = regexp.MustCompile(`^Would uninstall (.+):$`)

// Brewfile keywords for the headings brew bundle prints; an unknown heading
// (a new extension) is used as is.
var cleanupKinds = map[string]string{
	"casks":              "cask",
	"formulae":           "brew",
	"Mac App Store apps": "mas",
	"VSCode extensions":  "vscode",
	"Go packages":        "go",
	"Cargo packages":     "cargo",
	"npm packages":       "npm",
	"uv tools":           "uv",
	"Krew plugins":       "krew",
	"flatpaks":           "flatpak",
	"WinGet packages":    "winget",
}

// ParseCleanup extracts what `brew bundle cleanup` (dry run) would remove.
// Cache pruning ("Would `brew cleanup`") is ignored as it is not state we
// manage.
func ParseCleanup(out string) []string {
	var res []string
	kind := ""
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if line == "Would untap:" {
			kind = "tap"
			continue
		}
		if g := uninstallHeading.FindStringSubmatch(line); g != nil {
			kind = cmp.Or(cleanupKinds[g[1]], g[1])
			continue
		}
		if strings.HasPrefix(line, "Would ") || strings.HasPrefix(line, "Run ") || line == "" {
			kind = ""
			continue
		}
		if kind != "" {
			res = append(res, kind+" "+line)
		}
	}
	return res
}

// Entry is one tap/brew/cask line of a Brewfile.
type Entry struct {
	Kind    string
	Name    string
	Trusted bool
}

var (
	entryLine   = regexp.MustCompile(`^(tap|brew|cask)\s+"([^"]+)"`)
	trustedTrue = regexp.MustCompile(`,\s*trusted:\s*true\b`)
)

// ParseBrewfile reads the tap/brew/cask entries of a Brewfile. Other
// directives (mas, vscode, …) are left to brew bundle.
func ParseBrewfile(file string) ([]Entry, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ParseEntries(data), nil
}

// ParseEntries reads the tap/brew/cask entries of Brewfile contents.
func ParseEntries(data []byte) []Entry {
	var entries []Entry
	for line := range strings.Lines(string(data)) {
		if g := entryLine.FindStringSubmatch(strings.TrimSpace(line)); g != nil {
			code, _ := splitComment(line)
			entries = append(entries, Entry{Kind: g[1], Name: g[2], Trusted: trustedTrue.MatchString(code)})
		}
	}
	return entries
}

// TrustTaps marks every third-party tap line without a `trusted:` option as
// trusted, for a Brewfile dumped from a machine that already has the taps,
// and returns the taps it changed. An existing `trusted:` option (e.g.
// trusting only some formulae) is left alone rather than widened.
func TrustTaps(data []byte) ([]byte, []string) {
	var out strings.Builder
	var changed []string
	for line := range strings.Lines(string(data)) {
		body, nl := strings.CutSuffix(line, "\n")
		code, comment := splitComment(body)
		g := entryLine.FindStringSubmatch(strings.TrimSpace(code))
		if g == nil || !thirdParty(Entry{Kind: g[1], Name: g[2]}) || trustedOption.MatchString(code) {
			out.WriteString(line)
			continue
		}
		trimmed := strings.TrimRight(code, " \t")
		out.WriteString(trimmed + ", trusted: true" + code[len(trimmed):] + comment)
		if nl {
			out.WriteString("\n")
		}
		changed = append(changed, g[2])
	}
	return []byte(out.String()), changed
}

var trustedOption = regexp.MustCompile(`,\s*trusted:`)

// splitComment splits a Brewfile (Ruby) line at the first # outside a
// string literal.
func splitComment(line string) (code, comment string) {
	var quote rune
	escaped := false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case quote != 0 && r == '\\':
			escaped = true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return line[:i], line[i:]
		}
	}
	return line, ""
}
