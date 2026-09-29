// Package config loads and validates zakwas.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the config file zakwas looks for.
const FileName = "zakwas.yaml"

type Config struct {
	Protect   Protect   `yaml:"protect"`
	Files     []Link    `yaml:"files"`
	Links     []Link    `yaml:"links"`
	Templates Templates `yaml:"templates"`
	Brew      *Brew     `yaml:"brew"`
	Mise      *Mise     `yaml:"mise"`
	Defaults  []Default `yaml:"defaults"`
	System    System    `yaml:"system"`
	Commands  []Command `yaml:"commands"`

	// Root is the directory holding zakwas.yaml; relative sources resolve from it.
	Root string `yaml:"-"`
}

// Protect controls how files and templates are locked down. Write bits are
// always stripped; Immutable also sets the macOS uchg flag, so even the owner
// can't edit or delete them without `chflags nouchg`.
type Protect struct {
	Immutable bool `yaml:"immutable"`
}

// Link maps a repo source to a home destination. Under `files` it is a
// protected copy (directories are copied file by file); under `links` it is
// a symlink, for configs that apps must be able to write.
type Link struct {
	Src string `yaml:"src"`
	Dst string `yaml:"dst"`
}

type Templates struct {
	Vars  map[string]string `yaml:"vars"`
	Files []TemplateFile    `yaml:"files"`
}

type TemplateFile struct {
	Src  string      `yaml:"src"`
	Dst  string      `yaml:"dst"`
	Mode fs.FileMode `yaml:"mode"`
}

type Brew struct {
	File    string `yaml:"file"`
	Cleanup string `yaml:"cleanup"`
	Upgrade bool   `yaml:"upgrade"`
}

const (
	CleanupNone      = "none"
	CleanupUninstall = "uninstall"
	CleanupZap       = "zap"
)

// Mise points at the global mise config. Prune removes installed versions no
// mise config on the machine references anymore, like brew's zap cleanup.
type Mise struct {
	Config string `yaml:"config"`
	Prune  bool   `yaml:"prune"`
}

// Default is one `defaults write` entry. The value's YAML type selects the
// defaults type (bool, int, float, string). Restart names a process to
// killall after the value changes; empty uses the built-in domain mapping.
// CurrentHost targets the per-host (ByHost) preferences.
type Default struct {
	Domain      string `yaml:"domain"`
	Key         string `yaml:"key"`
	Value       any    `yaml:"value"`
	Restart     string `yaml:"restart"`
	CurrentHost bool   `yaml:"currentHost"`
}

type System struct {
	Dirs        []Dir   `yaml:"dirs"`
	SudoTouchID bool    `yaml:"sudoTouchID"`
	SSHKey      *SSHKey `yaml:"sshKey"`
}

type Dir struct {
	Path string      `yaml:"path"`
	Mode fs.FileMode `yaml:"mode"`
}

type SSHKey struct {
	Path    string `yaml:"path"`
	Comment string `yaml:"comment"`
}

// Command runs Run through sh whenever Check (also through sh) fails.
type Command struct {
	Name  string `yaml:"name"`
	Check string `yaml:"check"`
	Run   string `yaml:"run"`
}

// Load reads and validates the config at path; home expands "~" in
// destinations.
func Load(path, home string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	root, err := resolveRoot(path)
	if err != nil {
		return nil, err
	}
	c.Root = root
	if err := c.Validate(home); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

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
	var errs []error
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
		cl := claim{what: what, dst: dst, key: strings.ToLower(filepath.Clean(paths.Dst(dst)))}
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
				errs = append(errs, fmt.Errorf("%s[%d]: src and dst are required", section.name, i))
			}
			claimDst(l.Dst, fmt.Sprintf("%s[%d]", section.name, i))
		}
	}
	for i, t := range c.Templates.Files {
		if t.Src == "" || t.Dst == "" {
			errs = append(errs, fmt.Errorf("templates.files[%d]: src and dst are required", i))
		}
		claimDst(t.Dst, fmt.Sprintf("templates.files[%d]", i))
		if t.Mode != 0 && (t.Mode&^0o777 != 0 || t.Mode&0o400 == 0) {
			errs = append(errs, fmt.Errorf("templates.files[%d]: mode %s must be octal permissions readable by the owner, e.g. 0644", i, octal(t.Mode)))
		}
	}
	if c.Brew != nil {
		if c.Brew.File == "" {
			errs = append(errs, errors.New("brew.file is required"))
		}
		switch c.Brew.Cleanup {
		case "", CleanupNone, CleanupUninstall, CleanupZap:
		default:
			errs = append(errs, fmt.Errorf("brew.cleanup: %q is not one of none, uninstall, zap", c.Brew.Cleanup))
		}
	}
	if c.Mise != nil && c.Mise.Config == "" {
		errs = append(errs, errors.New("mise.config is required"))
	}
	type defaultKey struct {
		domain, key string
		currentHost bool
	}
	defaultsSeen := map[defaultKey]int{}
	for i, d := range c.Defaults {
		if d.Domain == "" || d.Key == "" {
			errs = append(errs, fmt.Errorf("defaults[%d]: domain and key are required", i))
		}
		key := defaultKey{d.Domain, d.Key, d.CurrentHost}
		if first, dup := defaultsSeen[key]; dup {
			errs = append(errs, fmt.Errorf("defaults[%d]: %s %s is already set by defaults[%d]", i, d.Domain, d.Key, first))
		} else {
			defaultsSeen[key] = i
		}
		switch d.Value.(type) {
		case bool, int, float64, string:
		default:
			errs = append(errs, fmt.Errorf("defaults[%d] %s %s: unsupported value %#v", i, d.Domain, d.Key, d.Value))
		}
	}
	for i, d := range c.System.Dirs {
		if d.Path == "" {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: path is required", i))
		} else if err := checkDst(paths, d.Path); err != nil {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: %w", i, err))
		}
		if d.Mode != 0 && (d.Mode&^0o777 != 0 || d.Mode&0o500 != 0o500) {
			errs = append(errs, fmt.Errorf("system.dirs[%d]: mode %s must be octal permissions the owner can read and enter, e.g. 0700", i, octal(d.Mode)))
		}
	}
	if k := c.System.SSHKey; k != nil {
		if k.Path == "" {
			errs = append(errs, errors.New("system.sshKey.path is required"))
		} else if err := checkDst(paths, k.Path); err != nil {
			errs = append(errs, fmt.Errorf("system.sshKey: %w", err))
		}
	}
	for i, cmd := range c.Commands {
		if cmd.Name == "" || cmd.Check == "" || cmd.Run == "" {
			errs = append(errs, fmt.Errorf("commands[%d]: name, check and run are required", i))
		}
	}
	return errors.Join(errs...)
}

type claim struct{ what, dst, key string }

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
}

// Dst expands a destination path.
func (p Paths) Dst(path string) string {
	if path == "~" {
		return p.Home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
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

// Pretty shortens an absolute path under home back to "~/...".
func (p Paths) Pretty(path string) string {
	if rest, ok := strings.CutPrefix(path, p.Home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}
