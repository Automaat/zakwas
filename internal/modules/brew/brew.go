// Package brew converges Homebrew to a Brewfile via `brew bundle`.
package brew

import (
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
	file := m.Paths.Src(m.Brew.File)
	entries, err := ParseBrewfile(file)
	if err != nil {
		return nil, err
	}
	if !m.Runner.Installed("brew") {
		return m.planBootstrap(file, entries)
	}

	if err := requireTrust(entries); err != nil {
		return nil, err
	}
	trust, err := m.planTrust(ctx, entries)
	if err != nil {
		return nil, err
	}

	var install []engine.Change
	missing, err := m.missing(ctx, file)
	if err != nil {
		return nil, err
	}
	for _, e := range missing {
		install = append(install, engine.Change{Action: engine.Create, Target: e})
	}
	if m.Brew.Upgrade {
		outdated, err := m.outdated(ctx, entries)
		if err != nil {
			return nil, err
		}
		for _, e := range outdated {
			install = append(install, engine.Change{Action: engine.Update, Target: e.Name, From: e.From, To: e.To})
		}
	}
	if len(install) > 0 {
		install = append(install, m.bundleInstall(file))
	}

	cleanup, err := m.planCleanup(ctx, file)
	if err != nil {
		return nil, err
	}
	return append(append(trust, install...), cleanup...), nil
}

// installerCommit pins the Homebrew installer script, so a compromised or
// broken upstream HEAD can't reach machines until Renovate proposes it.
// renovate: datasource=git-refs depName=https://github.com/Homebrew/install branch=main
const installerCommit = "7a3f48c7e498d7df3237fd41c30f4d492857edf4"

// homebrewInstall primes sudo while a terminal is attached, so the official
// installer can run NONINTERACTIVE (no "press RETURN") with cached credentials.
const homebrewInstall = `sudo -v && NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/` +
	installerCommit + `/install.sh)"`

// planBootstrap installs Homebrew, then everything in the Brewfile; taps are
// trusted by `brew bundle install` from their `trusted:` options. A fresh
// Homebrew has nothing to clean up.
func (m *Module) planBootstrap(file string, entries []Entry) ([]engine.Change, error) {
	if err := requireTrust(entries); err != nil {
		return nil, err
	}
	cmd := runner.Cmd{Name: "/bin/bash", Args: []string{"-c", homebrewInstall}, Stream: true}
	changes := []engine.Change{{
		Action: engine.Create, Target: "Homebrew", Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}}
	for _, e := range entries {
		changes = append(changes, engine.Change{Action: engine.Create, Target: e.Kind + " " + e.Name})
	}
	return append(changes, m.bundleInstall(file)), nil
}

type trustJSON struct {
	Taps []string `json:"taps"`
}

func thirdParty(e Entry) bool {
	return e.Kind == "tap" && !strings.HasPrefix(e.Name, "homebrew/")
}

// requireTrust makes the Brewfile the only source of tap trust: `brew bundle
// cleanup --force` resets Homebrew's trust store to the Brewfile's `trusted:`
// options, so trust granted any other way is wiped on the next cleanup and
// installs from the tap start failing.
func requireTrust(entries []Entry) error {
	var errs []error
	for _, e := range entries {
		if thirdParty(e) && !e.Trusted {
			errs = append(errs, fmt.Errorf("tap %q in the Brewfile needs `trusted: true`", e.Name))
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
		if thirdParty(e) {
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

func (m *Module) bundleInstall(file string) engine.Change {
	args := []string{"bundle", "install", "--file", file}
	if !m.Brew.Upgrade {
		args = append(args, "--no-upgrade")
	}
	cmd := runner.Cmd{Name: "brew", Args: args, Env: noAutoUpdate, Stream: true}
	return engine.Change{
		Action: engine.Run, Target: "brew bundle install", Streams: true,
		Apply: func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
	}
}

var missingLine = regexp.MustCompile(`^→ (\S+) (\S+) needs to be `)

func (m *Module) missing(ctx context.Context, file string) ([]string, error) {
	cmd := runner.Cmd{Name: "brew", Args: []string{"bundle", "check", "--file", file, "--verbose", "--no-upgrade"}, Env: noAutoUpdate}
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
			out = append(out, strings.ToLower(g[1])+" "+g[2])
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

func (m *Module) planCleanup(ctx context.Context, file string) ([]engine.Change, error) {
	mode := m.Brew.Cleanup
	if mode == "" || mode == config.CleanupNone {
		return nil, nil
	}
	dryRun := runner.Cmd{Name: "brew", Args: []string{"bundle", "cleanup", "--file", file}, Env: noAutoUpdate}
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
	args := []string{"bundle", "cleanup", "--force", "--file", file}
	if mode == config.CleanupZap {
		args = append(args, "--zap")
	}
	cmd := runner.Cmd{Name: "brew", Args: args, Env: noAutoUpdate, Stream: true}
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
	var entries []Entry
	for line := range strings.Lines(string(data)) {
		if g := entryLine.FindStringSubmatch(strings.TrimSpace(line)); g != nil {
			code, _ := splitComment(line)
			entries = append(entries, Entry{Kind: g[1], Name: g[2], Trusted: trustedTrue.MatchString(code)})
		}
	}
	return entries, nil
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
