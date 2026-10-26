// Package config loads and validates zakwas.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/platform"
)

// FileName is the config file zakwas looks for.
const FileName = "zakwas.yaml"

type Config struct {
	Protect   Protect      `yaml:"protect" jsonschema_description:"How files and templates are locked down after zakwas writes them."`
	Files     []Link       `yaml:"files" jsonschema_description:"Repo files installed into your home directory as protected, read-only copies. A directory src copies every file inside it. Edit the source in the repo and run 'zakwas apply' to change one."`
	Links     []Link       `yaml:"links" jsonschema_description:"Repo files symlinked into your home directory, for configs an app must be able to write itself. Links are not protected."`
	Templates Templates    `yaml:"templates" jsonschema_description:"Go text/template files rendered into your home directory as protected copies, for dotfiles that need your home path or per-machine values."`
	Brew      *Brew        `yaml:"brew" jsonschema_description:"Homebrew packages from a Brewfile. Homebrew is installed first when missing. Omit the section to leave Homebrew alone."`
	Mise      *Mise        `yaml:"mise" jsonschema_description:"Tools pinned in a global mise config. mise is installed first when missing. Omit the section to leave mise alone."`
	Defaults  []Default    `yaml:"defaults" jsonschema_description:"macOS preferences written with 'defaults write'. Each domain + key (+ currentHost) may appear once."`
	System    System       `yaml:"system" jsonschema_description:"Machine-level setup: directories, Touch ID for sudo, an SSH key."`
	Commands  []Command    `yaml:"commands" jsonschema_description:"One-off setup steps: 'run' executes only while 'check' fails. Entries run in order and stop at the first failure."`
	Launchd   []LaunchdJob `yaml:"launchd" jsonschema_description:"launchd user agents for scheduled jobs, such as a daily drift check. Each job's plist is written to ~/Library/LaunchAgents and loaded; changing a job reloads it, and removing it unloads it and deletes its plist. Only jobs zakwas created are ever unloaded or deleted. Labels must be unique."`
	Agents    *Agents      `yaml:"agents" jsonschema_description:"Coding-agent plugins and the marketplaces they come from, converged per provider: claude (Claude Code), codex (Codex) and opencode (skills linked into its skills directory). Omit the section to leave agent plugins alone."`

	// Root is the directory holding zakwas.yaml; relative sources resolve from it.
	Root string `yaml:"-"`
	Path string `yaml:"-"`

	// GOOS is the operating system the config is validated for; empty
	// means the one zakwas runs on.
	GOOS string `yaml:"-"`

	// Layers are the local layers merged into this config, in registration
	// order; see ParseLayered.
	Layers []LoadedLayer `yaml:"-"`

	// origins labels the entries of a merged config by the file they came
	// from, per section, so errors can name it.
	origins map[string][]string
}

// OS is the operating system the config is validated for.
func (c *Config) OS() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return platform.Current()
}

// Protect controls how files and templates are locked down. Write bits are
// always stripped; Immutable also sets the macOS uchg flag, so even the owner
// can't edit or delete them without `chflags nouchg`.
type Protect struct {
	Immutable bool `yaml:"immutable" jsonschema:"default=false" jsonschema_description:"Also set the macOS 'uchg' flag on installed files, so even the owner cannot edit, chmod or delete them without 'chflags nouchg'. Write bits are always stripped."`
}

// Link maps a repo source to a home destination. Under `files` it is a
// protected copy (directories are copied file by file); under `links` it is
// a symlink, for configs that apps must be able to write.
type Link struct {
	Src string `yaml:"src" jsonschema:"required,minLength=1" jsonschema_description:"Source path in the config repo, relative to the directory holding zakwas.yaml (or absolute). May be a directory under 'files'."`
	Dst string `yaml:"dst" jsonschema:"required,minLength=1" jsonschema_description:"Destination path: absolute or starting with '~/' (expanded to your home directory). '$VAR' is not expanded. Must not overlap another files/links/templates destination (compared case-insensitively). An existing file zakwas did not write is backed up to '<dst>.zakwas-bak'."`
}

type Templates struct {
	Vars  map[string]string `yaml:"vars" jsonschema_description:"Values available to every template as '{{ .Vars.<name> }}'. A template referencing a missing var fails."`
	Files []TemplateFile    `yaml:"files" jsonschema_description:"Templates to render. Each template also sees '{{ .Home }}', your home directory, and can call the agentPermissions function to render agents.permissions as opencode's permission.bash object and the agentMCP function to render agents.mcp as its mcp object."`
}

type TemplateFile struct {
	Src  string      `yaml:"src" jsonschema:"required,minLength=1" jsonschema_description:"Template path in the config repo (Go text/template syntax), relative to the directory holding zakwas.yaml (or absolute)."`
	Dst  string      `yaml:"dst" jsonschema:"required,minLength=1" jsonschema_description:"Destination path: absolute or starting with '~/' (expanded to your home directory). '$VAR' is not expanded. Must not overlap another files/links/templates destination."`
	Mode fs.FileMode `yaml:"mode" jsonschema:"minimum=0,maximum=511" jsonschema_description:"Octal permissions of the rendered file, written with the 0o prefix, e.g. 0o755. Editors parse YAML 1.2 and read 0755 as decimal 755 (zakwas still accepts it as octal); 755 is decimal everywhere. Must be readable by the owner. Write bits are always stripped. Default 0o444."`
}

type Brew struct {
	File    string `yaml:"file" jsonschema:"required,minLength=1" jsonschema_description:"Brewfile path in the config repo, relative to the directory holding zakwas.yaml (or absolute). Every third-party tap in it needs 'trusted: true'."`
	Cleanup string `yaml:"cleanup" jsonschema:"enum=none,enum=uninstall,enum=zap,default=none" jsonschema_description:"What to do with installed packages missing from the Brewfile: 'none' keeps them, 'uninstall' removes them ('brew bundle cleanup --force'), 'zap' also removes cask app data ('--zap'). Removals are shown in 'zakwas plan' first. Default none."`
	Upgrade bool   `yaml:"upgrade" jsonschema:"default=false" jsonschema_description:"Upgrade outdated Brewfile packages on apply. When false, only missing packages are installed ('--no-upgrade'). Run 'zakwas upgrade' to refresh Homebrew's package list first. Default false."`
}

const (
	CleanupNone      = "none"
	CleanupUninstall = "uninstall"
	CleanupZap       = "zap"
)

// Mise points at the global mise config. Prune removes installed versions no
// mise config on the machine references anymore, like brew's zap cleanup.
type Mise struct {
	Config string `yaml:"config" jsonschema:"required,minLength=1" jsonschema_description:"Global mise config (config.toml) in the config repo, relative to the directory holding zakwas.yaml (or absolute). Its tools are installed with 'mise install'. Install it to ~/.config/mise/config.toml with a 'files' entry so mise uses it outside zakwas too."`
	Prune  bool   `yaml:"prune" jsonschema:"default=false" jsonschema_description:"Remove installed tool versions that no mise config on the machine references anymore ('mise prune'). Default false."`
}

// Default is one `defaults write` entry. The value's YAML type selects the
// defaults type (bool, int, float, string). Restart names a process to
// killall after the value changes; empty uses the built-in domain mapping.
// CurrentHost targets the per-host (ByHost) preferences.
type Default struct {
	Domain      string `yaml:"domain" jsonschema:"required,minLength=1" jsonschema_description:"Preferences domain, e.g. 'com.apple.dock' or 'NSGlobalDomain'."`
	Key         string `yaml:"key" jsonschema:"required,minLength=1" jsonschema_description:"Preference key within the domain, e.g. 'autohide'."`
	Value       any    `yaml:"value" jsonschema:"required,oneof_type=boolean;number;string" jsonschema_description:"Value to write. The YAML type picks the defaults type: true/false → -bool, 2 → -int, 1.5 → -float, anything else → -string. Quote dates (\"2024-01-01\") and numbers meant as text (\"2\"); an unquoted date is rejected."`
	Restart     string `yaml:"restart" jsonschema_description:"Process to 'killall' after the value changes, so it picks up the new setting. Empty uses the built-in mapping (com.apple.dock → Dock, com.apple.finder → Finder, com.apple.screencapture → SystemUIServer)."`
	CurrentHost bool   `yaml:"currentHost" jsonschema:"default=false" jsonschema_description:"Write to the per-host (ByHost) preferences, like 'defaults -currentHost write'. Default false."`
}

type System struct {
	Dirs        []Dir   `yaml:"dirs" jsonschema_description:"Directories to create."`
	SudoTouchID bool    `yaml:"sudoTouchID" jsonschema:"default=false" jsonschema_description:"Allow Touch ID for sudo by adding pam_tid.so to /etc/pam.d/sudo_local, which survives macOS updates. Default false."`
	SSHKey      *SSHKey `yaml:"sshKey" jsonschema_description:"Generate an ed25519 SSH key (no passphrase) when the file does not exist. An existing key is never touched."`
}

type Dir struct {
	Path string      `yaml:"path" jsonschema:"required,minLength=1" jsonschema_description:"Directory path: absolute or starting with '~/' (expanded to your home directory). '$VAR' is not expanded. Missing parents are created."`
	Mode fs.FileMode `yaml:"mode" jsonschema:"minimum=0,maximum=511" jsonschema_description:"Octal permissions, written with the 0o prefix, e.g. 0o700. Editors parse YAML 1.2 and read 0700 as decimal 700 (zakwas still accepts it as octal); 700 is decimal everywhere. The owner must be able to read and enter it. When set, it is enforced on an existing directory too. When omitted, a new directory gets 0o755 and an existing one keeps its mode."`
}

type SSHKey struct {
	Path    string `yaml:"path" jsonschema:"required,minLength=1" jsonschema_description:"Private key path: absolute or starting with '~/' (expanded to your home directory), e.g. '~/.ssh/id_ed25519'."`
	Comment string `yaml:"comment" jsonschema_description:"Key comment ('ssh-keygen -C'), usually your email."`
}

// Shells a command can name. Unset means sh, or PowerShell on Windows.
const (
	ShellSh         = "sh"
	ShellBash       = "bash"
	ShellCmd        = "cmd"
	ShellPowerShell = "powershell"
	ShellPwsh       = "pwsh"
)

// Shells lists the values of Command.Shell.
var Shells = []string{ShellSh, ShellBash, ShellCmd, ShellPowerShell, ShellPwsh}

// Command runs Run through a shell whenever Check (through the same shell)
// fails.
type Command struct {
	Name  string `yaml:"name" jsonschema:"required,minLength=1" jsonschema_description:"Label shown in plan and apply output."`
	Check string `yaml:"check" jsonschema:"required,minLength=1" jsonschema_description:"Shell snippet (run in your home directory) that exits 0 when the step is already done. Times out after 30s."`
	Run   string `yaml:"run" jsonschema:"required,minLength=1" jsonschema_description:"Shell snippet (run in your home directory) executed while 'check' fails. 'check' must pass afterwards, or apply fails."`
	Shell string `yaml:"shell" jsonschema:"enum=sh,enum=bash,enum=cmd,enum=powershell,enum=pwsh" jsonschema_description:"Shell for 'check' and 'run': sh, bash, cmd, powershell (Windows PowerShell 5.1) or pwsh (PowerShell 7). Default: sh on macOS and Linux; on Windows pwsh when it is on PATH, else powershell. cmd runs a single line. The plan fails when the shell is not installed."`
}

// Load reads and validates the config at path; home expands "~" in
// destinations.
func Load(path, home string) (*Config, error) {
	return LoadFor(path, home, "")
}

// LoadFor is Load for the operating system goos; empty means this one.
func LoadFor(path, home, goos string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFor(data, path, home, goos)
}

// Parse validates config bytes as if they were the file at path, so an edit
// can be checked before it is written.
func Parse(data []byte, path, home string) (*Config, error) {
	return ParseFor(data, path, home, "")
}

// ParseFor is Parse for the operating system goos; empty means this one.
func ParseFor(data []byte, path, home, goos string) (*Config, error) {
	c, err := decode(data, path, goos)
	if err != nil {
		return nil, err
	}
	if err := c.Validate(home); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// decode reads config bytes without validating them.
func decode(data []byte, path, goos string) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, YAMLError(err))
	}
	if err := rejectNullProfileNames(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, YAMLError(err))
	}
	root, err := resolveRoot(path)
	if err != nil {
		return nil, err
	}
	c.Root = root
	c.Path = filepath.Join(root, filepath.Base(path))
	c.GOOS = goos
	return &c, nil
}

// RootOf is the directory a config file at path counts as the root of: its
// own directory with symlinks followed, the key the layer registry uses.
func RootOf(path string) (string, error) { return resolveRoot(path) }

// resolveRoot follows symlinks so link targets are identical no matter which
// path (e.g. a ~/.config symlink to the repo) zakwas was started from.
func resolveRoot(configPath string) (string, error) {
	abs, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// Find walks up from dir looking for zakwas.yaml.
func Find(dir string) (string, error) {
	for {
		p := filepath.Join(dir, FileName)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found", FileName)
		}
		dir = parent
	}
}

// Validate reports every structural problem at once.
func (c *Config) Validate(home string) error {
	goos := c.OS()
	errs := c.platformErrors(goos)
	here := func(module string) bool { return platform.Supported(module, goos) }
	paths := Paths{Home: home, Root: c.Root}
	var claimed []claim
	claimDst := func(dst, what string) {
		if dst == "" {
			return
		}
		if err := checkDst(paths, dst); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			return
		}
		cl := claim{what: what, dst: dst, key: foldPath(paths.Dst(dst))}
		for _, other := range claimed {
			if overlaps(cl.key, other.key) {
				errs = append(errs, fmt.Errorf("%s: destination %q overlaps %s %q", what, dst, other.what, other.dst))
			}
		}
		claimed = append(claimed, cl)
	}
	for _, section := range []struct {
		name    string
		entries []Link
	}{{"files", c.Files}, {"links", c.Links}} {
		for i, l := range section.entries {
			if l.Src == "" || l.Dst == "" {
				errs = append(errs, fmt.Errorf("%s: src and dst are required", c.where(section.name, i)))
			}
			claimDst(l.Dst, c.where(section.name, i))
		}
	}
	for i, t := range c.Templates.Files {
		if t.Src == "" || t.Dst == "" {
			errs = append(errs, fmt.Errorf("%s: src and dst are required", c.where("templates.files", i)))
		}
		claimDst(t.Dst, c.where("templates.files", i))
		if t.Mode != 0 && (t.Mode&^0o777 != 0 || t.Mode&0o400 == 0) {
			errs = append(errs, fmt.Errorf("%s: mode %s must be octal permissions readable by the owner, e.g. 0o644", c.where("templates.files", i), octal(t.Mode)))
		}
	}
	if c.Brew != nil && here("brew") {
		if c.Brew.File == "" {
			errs = append(errs, errors.New("brew.file is required"))
		}
		switch c.Brew.Cleanup {
		case "", CleanupNone, CleanupUninstall, CleanupZap:
		default:
			errs = append(errs, fmt.Errorf("brew.cleanup: %q is not one of none, uninstall, zap", c.Brew.Cleanup))
		}
	}
	if c.Mise != nil && here("mise") && c.Mise.Config == "" {
		errs = append(errs, errors.New("mise.config is required"))
	}
	type defaultKey struct {
		domain, key string
		currentHost bool
	}
	defaultsSeen := map[defaultKey]int{}
	for i, d := range c.Defaults {
		if !here("defaults") {
			break
		}
		if d.Domain == "" || d.Key == "" {
			errs = append(errs, fmt.Errorf("%s: domain and key are required", c.where("defaults", i)))
		}
		key := defaultKey{d.Domain, d.Key, d.CurrentHost}
		if first, dup := defaultsSeen[key]; dup {
			errs = append(errs, fmt.Errorf("%s: %s %s is already set by %s", c.where("defaults", i), d.Domain, d.Key, c.where("defaults", first)))
		} else {
			defaultsSeen[key] = i
		}
		switch v := d.Value.(type) {
		case bool, int, float64, string:
		case time.Time:
			errs = append(errs, fmt.Errorf("%s %s %s: YAML reads %s as a date; quote it to write a string", c.where("defaults", i), d.Domain, d.Key, yamlDate(v)))
		default:
			errs = append(errs, fmt.Errorf("%s %s %s: unsupported value of type %T", c.where("defaults", i), d.Domain, d.Key, d.Value))
		}
	}
	for i, d := range c.System.Dirs {
		if d.Path == "" {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: path is required", i))
		} else if err := checkDst(paths, d.Path); err != nil {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: %w", i, err))
		}
		if d.Mode != 0 && (d.Mode&^0o777 != 0 || d.Mode&0o500 != 0o500) {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: mode %s must be octal permissions the owner can read and enter, e.g. 0o700", i, octal(d.Mode)))
		}
	}
	if k := c.System.SSHKey; k != nil {
		if k.Path == "" {
			errs = append(errs, errors.New("system.sshKey.path is required"))
		} else if err := checkDst(paths, k.Path); err != nil {
			errs = append(errs, fmt.Errorf("system.sshKey: %w", err))
		}
	}
	if c.Agents != nil {
		errs = append(errs, c.Agents.validate()...)
	}
	if here("launchd") {
		errs = append(errs, c.validateLaunchd(paths, claimDst)...)
	}
	for i, cmd := range c.Commands {
		if cmd.Name == "" || cmd.Check == "" || cmd.Run == "" {
			errs = append(errs, fmt.Errorf("commands[%d]: name, check and run are required", i))
		}
		if cmd.Shell != "" && !slices.Contains(Shells, cmd.Shell) {
			errs = append(errs, fmt.Errorf("commands[%d]: unknown shell %q (want %s)", i, cmd.Shell, strings.Join(Shells, ", ")))
		}
		if cmd.Shell == ShellCmd && (multiline(cmd.Check) || multiline(cmd.Run)) {
			errs = append(errs, fmt.Errorf("commands[%d]: shell cmd runs a single line; put several steps in a .cmd file and call it", i))
		}
	}
	return errors.Join(errs...)
}

// platformErrors names every section that needs another operating system
// than goos, one line each.
func (c *Config) platformErrors(goos string) []error {
	used := map[string]bool{
		"system":   len(c.System.Dirs) > 0 || c.System.SSHKey != nil,
		"links":    len(c.Links) > 0,
		"commands": len(c.Commands) > 0,
		"brew":     c.Brew != nil,
		"mise":     c.Mise != nil,
		"launchd":  len(c.Launchd) > 0,
		"defaults": len(c.Defaults) > 0,
	}
	var errs []error
	for _, m := range platform.Modules {
		if !used[m.Name] {
			continue
		}
		if err := platform.ModuleError(m.Name, goos); err != nil {
			errs = append(errs, err)
		}
	}
	if c.System.SudoTouchID {
		if err := platform.FeatureError("system.sudoTouchID", goos); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Agents != nil && len(c.Agents.Skills) > 0 {
		if err := platform.FeatureError("agents.skills", goos); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// yamlDate prints a timestamp the way it was most likely written.
func yamlDate(t time.Time) string {
	if t.Equal(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)) {
		return t.Format(time.DateOnly)
	}
	return t.Format(time.RFC3339)
}

type claim struct{ what, dst, key string }

// Destination is a path a files, links or templates entry writes: What names
// the entry, Dst is as written and Path is absolute.
type Destination struct{ What, Dst, Path string }

// Destinations lists where the files, links and templates entries write,
// for the modules that write files of their own and must not share them.
func (c *Config) Destinations(paths Paths) []Destination {
	var out []Destination
	add := func(what, dst string) {
		if dst != "" {
			out = append(out, Destination{What: what, Dst: dst, Path: paths.Dst(dst)})
		}
	}
	for i, f := range c.Files {
		add(c.where("files", i), f.Dst)
	}
	for i, l := range c.Links {
		add(c.where("links", i), l.Dst)
	}
	for i, t := range c.Templates.Files {
		add(c.where("templates.files", i), t.Dst)
	}
	return out
}

// PathsOverlap reports whether two absolute paths are the same file, or one
// is inside the other, ignoring case. Symlinks in the directories that
// exist are followed, so two spellings of one file overlap, and so is a
// last element that is a symlink to a directory, which a write goes
// through. A last element that is a symlink to anything else is compared
// as written, since a write replaces the link, and the part of a path that
// does not exist yet is compared lexically.
func PathsOverlap(a, b string) bool {
	return KeysOverlap(PathKey(a), PathKey(b))
}

// PathKey is the form of an absolute path PathsOverlap compares, for a
// caller that checks one path against many and resolves each only once.
func PathKey(path string) string { return foldPath(resolvePath(path)) }

// KeysOverlap is PathsOverlap on two PathKey results.
func KeysOverlap(a, b string) bool { return overlaps(a, b) }

func resolvePath(path string) string {
	resolved := resolveParents(path)
	if info, err := os.Lstat(resolved); err == nil && info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		if target, err := os.Stat(resolved); err == nil && target.IsDir() {
			if full, err := filepath.EvalSymlinks(resolved); err == nil {
				return full
			}
		}
	}
	return resolved
}

func resolveParents(path string) string {
	path = filepath.Clean(path)
	orig := path
	rest := filepath.Base(path)
	dir := filepath.Dir(path)
	for dir != path {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		path = dir
		dir = filepath.Dir(dir)
	}
	return orig
}

func foldPath(path string) string { return strings.ToLower(filepath.Clean(path)) }

// overlaps compares case-folded paths, since APFS is case-insensitive by
// default: two entries on one path, or one inside a directory the other
// manages, would fight over the same files and backups.
func overlaps(a, b string) bool {
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

// checkDst requires a path that expands to an absolute one: "$VAR" is not
// expanded, and a relative path would land wherever zakwas runs from.
func checkDst(paths Paths, dst string) error {
	if strings.Contains(dst, "$") {
		return fmt.Errorf("path %q: environment variables are not expanded, use ~/", dst)
	}
	if !filepath.IsAbs(paths.Dst(dst)) {
		return fmt.Errorf("path %q must be absolute or start with ~/", dst)
	}
	return nil
}

// octal shows a mode both ways: "mode: 644" without the leading 0 decodes as
// decimal 644, i.e. octal 1204.
func octal(m fs.FileMode) string {
	return fmt.Sprintf("%d (octal %o)", uint32(m), uint32(m))
}

// Paths resolves user-facing paths: "~" against home, relative paths against
// the config root.
type Paths struct {
	Home string
	Root string
	// Layers are the roots of the local layers merged into the config. Their
	// relative sources are already absolute; they are listed so every check
	// that guards the config repo guards theirs too.
	Layers []string
}

// Roots lists every config repo root: the main one, then the layers'.
func (p Paths) Roots() []string {
	return append([]string{p.Root}, p.Layers...)
}

// CutHome splits "~/rest" into rest. Where backslash separates paths, "~\rest"
// does too. A bare "~" is not matched; callers compare it themselves.
func CutHome(path string) (rest string, ok bool) {
	if rest, ok = strings.CutPrefix(path, "~/"); ok {
		return rest, true
	}
	if filepath.Separator == '\\' {
		return strings.CutPrefix(path, `~\`)
	}
	return "", false
}

// IsHomePath reports whether path is "~" or starts with "~/".
func IsHomePath(path string) bool {
	_, ok := CutHome(path)
	return ok || path == "~"
}

// Dst expands a destination path.
func (p Paths) Dst(path string) string {
	if path == "~" {
		return p.Home
	}
	if rest, ok := CutHome(path); ok {
		return filepath.Join(p.Home, rest)
	}
	return path
}

// Src resolves a source path inside the repo.
func (p Paths) Src(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(p.Root, path)
}

// Pretty shortens an absolute path under home back to "~/...", with forward
// slashes on every platform. Windows paths match the home directory without
// regard to case.
func (p Paths) Pretty(path string) string {
	if rest, ok := CutDir(path, p.Home); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}

// CutDir returns the part of path below dir, ignoring case on Windows.
func CutDir(path, dir string) (rest string, ok bool) {
	return cutPathPrefix(path, dir, filepath.Separator == '\\')
}

func cutPathPrefix(path, dir string, fold bool) (rest string, ok bool) {
	prefix := dir + string(filepath.Separator)
	if len(path) < len(prefix) {
		return "", false
	}
	head := path[:len(prefix)]
	if head == prefix || fold && strings.EqualFold(head, prefix) {
		return path[len(prefix):], true
	}
	return "", false
}

func multiline(script string) bool {
	return strings.ContainsAny(strings.TrimRight(script, " \t\r\n"), "\r\n")
}
