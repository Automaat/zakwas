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
			install = append(install, engine.Change{Action: engine.Update, Target: e, Detail: "outdated"})
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
		Action: engine.Run, Target: "brew bundle install",
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
	Name   string `json:"name"`
	Pinned bool   `json:"pinned"`
}

type outdatedJSON struct {
	Formulae []outdatedEntry `json:"formulae"`
	Casks    []outdatedEntry `json:"casks"`
}

// outdated skips pinned entries: brew bundle doesn't upgrade them, so they
// would be drift that never converges.
func (m *Module) outdated(ctx context.Context, entries []Entry) ([]string, error) {
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
	var res []string
	add := func(kind string, list []outdatedEntry) {
		for _, e := range list {
			if key := kind + " " + path.Base(e.Name); wanted[key] && !e.Pinned {
				res = append(res, key)
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
		changes = append(changes, engine.Change{Action: engine.Remove, Target: r})
	}
	args := []string{"bundle", "cleanup", "--force", "--file", file}
	if mode == config.CleanupZap {
		args = append(args, "--zap")
	}
	cmd := runner.Cmd{Name: "brew", Args: args, Env: noAutoUpdate, Stream: true}
	return append(changes, engine.Change{
		Action: engine.Run, Target: "brew bundle cleanup",
		Detail: mode,
		Apply:  func(ctx context.Context) error { return runner.Check(ctx, m.Runner, cmd) },
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
			entries = append(entries, Entry{Kind: g[1], Name: g[2], Trusted: trustedTrue.MatchString(line)})
		}
	}
	return entries, nil
}
