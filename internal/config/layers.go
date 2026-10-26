package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
)

// Layer is a local layer registered on this machine: an extra zakwas.yaml
// merged into the config of the repo it was registered for. The repo never
// refers to it. Path is the layer's zakwas.yaml, absolute with symlinks
// resolved.
type Layer struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// LoadedLayer is a layer that merged into a config. Root is the directory
// holding the layer's zakwas.yaml, which its relative sources resolve against.
type LoadedLayer struct {
	Layer
	Root string
}

// MiseConfig is a mise config a layer brought: Path is absolute. Installed
// lists the destinations of the layer's files entries that copy it, which
// mise reads as global config once there.
type MiseConfig struct {
	Layer     string
	Path      string
	Installed []string
}

var layerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidLayerName reports whether name is usable for a layer: lowercase
// letters, digits and '-', not starting with '-'.
func ValidLayerName(name string) bool { return layerName.MatchString(name) }

// LayerError says why a registered layer could not be merged. Missing means
// the layer's file does not exist.
type LayerError struct {
	Layer   Layer
	Missing bool
	Err     error
}

func (e *LayerError) Unwrap() error { return e.Err }

// Detail is Error without the hint to unregister the layer.
func (e *LayerError) Detail() string {
	if e.Missing {
		return fmt.Sprintf("layer %q: %s does not exist", e.Layer.Name, e.Layer.Path)
	}
	return fmt.Sprintf("layer %q is invalid:\n%v", e.Layer.Name, e.Err)
}

// Error names the layer and the command that drops it. A layer is never
// skipped: leaving it out would make the next apply remove what it installed.
func (e *LayerError) Error() string {
	if e.Missing {
		return fmt.Sprintf("layer %q: %s does not exist; restore it or run `zakwas layer remove %s`", e.Layer.Name, e.Layer.Path, e.Layer.Name)
	}
	return fmt.Sprintf("layer %q is invalid; fix it or run `zakwas layer remove %s`:\n%v", e.Layer.Name, e.Layer.Name, e.Err)
}

// LayerRefs lists the layers merged into the config, as they were registered.
func (c *Config) LayerRefs() []Layer {
	out := make([]Layer, len(c.Layers))
	for i, l := range c.Layers {
		out[i] = l.Layer
	}
	return out
}

// LayerRoots lists the directories holding the merged layers' configs.
func (c *Config) LayerRoots() []string {
	out := make([]string, len(c.Layers))
	for i, l := range c.Layers {
		out[i] = l.Root
	}
	return out
}

// LayerMise lists the mise configs the layers brought, in registration order.
func (c *Config) LayerMise() []MiseConfig { return slices.Clone(c.layerMise) }

// Paths resolves paths for the config: its root and its layers' roots.
func (c *Config) Paths(home string) Paths {
	return Paths{Home: home, Root: c.Root, Layers: c.LayerRoots()}
}

// LoadLayered is LoadFor with local layers merged in; see ParseLayered.
func LoadLayered(path, home, goos string, layers []Layer) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseLayered(data, path, home, goos, layers)
}

// ParseLayered is ParseFor with the layers merged in registration order. It
// is the one place a config with layers is built: every command that plans
// or applies, and every proposal check, goes through it.
//
// Each layer is a zakwas.yaml of its own, validated on its own, whose
// relative sources resolve against its own directory. Their files, links,
// templates, commands, launchd jobs, defaults and system directories follow
// the main config's; templates.vars merge, with a repeated name an error;
// system.sshKey may be set once; sudoTouchID is on when any file sets it.
// A layer may set mise.config when the main config has a mise section; a
// tool pinned in two mise configs is an error naming both files. protect,
// brew, agents and mise.prune belong to the main config only. The merged
// config is validated as a whole, so a destination two files both claim is
// an error naming both. A layer that is missing or invalid fails the load.
func ParseLayered(data []byte, path, home, goos string, layers []Layer) (*Config, error) {
	c, err := ParseFor(data, path, home, goos)
	if err != nil || len(layers) == 0 {
		return c, err
	}
	m := newMerge(c)
	var errs []error
	for _, l := range layers {
		lc, err := loadLayer(l, home, goos)
		if err != nil {
			return nil, err
		}
		errs = append(errs, m.add(l, lc)...)
	}
	if err := m.cfg.Validate(home); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, m.cfg.miseConflicts(home)...)
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m.cfg, nil
}

// merge accumulates the main config and its layers, remembering which file
// each template var and the ssh key came from.
type merge struct {
	cfg     *Config
	seen    map[string]bool
	varFile map[string]string
	sshFile string
}

func newMerge(c *Config) *merge {
	m := *c
	m.Files = slices.Clone(c.Files)
	m.Links = slices.Clone(c.Links)
	m.Templates.Files = slices.Clone(c.Templates.Files)
	m.Templates.Vars = make(map[string]string, len(c.Templates.Vars))
	m.Defaults = slices.Clone(c.Defaults)
	m.Commands = slices.Clone(c.Commands)
	m.Launchd = slices.Clone(c.Launchd)
	m.System.Dirs = slices.Clone(c.System.Dirs)
	m.Layers = nil
	m.layerMise = nil
	m.origins = map[string][]string{}
	mg := &merge{cfg: &m, seen: map[string]bool{c.Path: true}, varFile: map[string]string{}}
	mg.label("files", len(c.Files), c.Path)
	mg.label("links", len(c.Links), c.Path)
	mg.label("templates.files", len(c.Templates.Files), c.Path)
	mg.label("defaults", len(c.Defaults), c.Path)
	mg.label("launchd", len(c.Launchd), c.Path)
	for k, v := range c.Templates.Vars {
		m.Templates.Vars[k] = v
		mg.varFile[k] = c.Path
	}
	if c.System.SSHKey != nil {
		mg.sshFile = c.Path
	}
	return mg
}

func (m *merge) label(section string, n int, file string) {
	for i := range n {
		m.cfg.origins[section] = append(m.cfg.origins[section], fmt.Sprintf("%s[%d] of %s", section, i, file))
	}
}

func (m *merge) add(l Layer, lc *Config) []error {
	c := m.cfg
	if m.seen[lc.Path] {
		return []error{fmt.Errorf("layer %q: %s is already part of this config", l.Name, lc.Path)}
	}
	m.seen[lc.Path] = true
	c.Layers = append(c.Layers, LoadedLayer{Layer: l, Root: lc.Root})
	abs := func(src string) string {
		if src == "" || filepath.IsAbs(src) {
			return src
		}
		return filepath.Join(lc.Root, src)
	}
	for _, f := range lc.Files {
		f.Src = abs(f.Src)
		c.Files = append(c.Files, f)
	}
	for _, f := range lc.Links {
		f.Src = abs(f.Src)
		c.Links = append(c.Links, f)
	}
	for _, t := range lc.Templates.Files {
		t.Src = abs(t.Src)
		c.Templates.Files = append(c.Templates.Files, t)
	}
	c.Defaults = append(c.Defaults, lc.Defaults...)
	c.Launchd = append(c.Launchd, lc.Launchd...)
	c.Commands = append(c.Commands, lc.Commands...)
	c.System.Dirs = append(c.System.Dirs, lc.System.Dirs...)
	c.System.SudoTouchID = c.System.SudoTouchID || lc.System.SudoTouchID
	var errs []error
	if lc.Mise != nil {
		if c.Mise == nil {
			errs = append(errs, fmt.Errorf("layer %q: mise.config in %s needs a mise section in %s", l.Name, lc.Path, c.Path))
		} else {
			mc := MiseConfig{Layer: l.Name, Path: abs(lc.Mise.Config)}
			for _, f := range lc.Files {
				if filepath.Clean(abs(f.Src)) == filepath.Clean(mc.Path) {
					mc.Installed = append(mc.Installed, f.Dst)
				}
			}
			c.layerMise = append(c.layerMise, mc)
		}
	}
	m.label("files", len(lc.Files), lc.Path)
	m.label("links", len(lc.Links), lc.Path)
	m.label("templates.files", len(lc.Templates.Files), lc.Path)
	m.label("defaults", len(lc.Defaults), lc.Path)
	m.label("launchd", len(lc.Launchd), lc.Path)
	for _, k := range slices.Sorted(maps.Keys(lc.Templates.Vars)) {
		if first, dup := m.varFile[k]; dup {
			errs = append(errs, fmt.Errorf("templates.vars.%s is set in both %s and %s", k, first, lc.Path))
			continue
		}
		c.Templates.Vars[k] = lc.Templates.Vars[k]
		m.varFile[k] = lc.Path
	}
	if lc.System.SSHKey != nil {
		if m.sshFile != "" {
			errs = append(errs, fmt.Errorf("system.sshKey is set in both %s and %s; set it once", m.sshFile, lc.Path))
		} else {
			c.System.SSHKey, m.sshFile = lc.System.SSHKey, lc.Path
		}
	}
	return errs
}

// loadLayer reads and validates one layer on its own.
func loadLayer(l Layer, home, goos string) (*Config, error) {
	data, err := os.ReadFile(l.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &LayerError{Layer: l, Missing: true, Err: err}
	}
	if err != nil {
		return nil, &LayerError{Layer: l, Err: err}
	}
	lc, err := decode(data, l.Path, goos)
	if err != nil {
		return nil, &LayerError{Layer: l, Err: err}
	}
	if errs := rootOnly(data); len(errs) > 0 {
		return nil, &LayerError{Layer: l, Err: fmt.Errorf("%s: %w", l.Path, errors.Join(errs...))}
	}
	if err := lc.Validate(home); err != nil {
		return nil, &LayerError{Layer: l, Err: fmt.Errorf("%s: %w", l.Path, err)}
	}
	return lc, nil
}

// rootOnly names every section a layer sets that only the main config may,
// by the keys present in its YAML: an empty value still counts as set.
func rootOnly(data []byte) []error {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil
	}
	var errs []error
	for _, name := range []string{"protect", "brew", "agents"} {
		if _, set := top[name]; set {
			errs = append(errs, fmt.Errorf("%s is set in a layer; only the main zakwas.yaml may set it", name))
		}
	}
	if node, set := top["mise"]; set {
		errs = append(errs, miseRootOnly(node)...)
	}
	return errs
}

// miseRootOnly allows only mise.config in a layer: prune is machine-wide.
func miseRootOnly(node yaml.Node) []error {
	if node.Kind != yaml.MappingNode {
		return []error{errors.New("mise is set in a layer without mise.config; a layer may set only mise.config")}
	}
	var errs []error
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; key != "config" {
			errs = append(errs, fmt.Errorf("mise.%s is set in a layer; only the main zakwas.yaml may set it", key))
		}
	}
	return errs
}

// miseConflicts reads the mise configs of a merged config and names every
// tool pinned in more than one: mise would pick one silently.
func (c *Config) miseConflicts(home string) []error {
	if c.Mise == nil || len(c.layerMise) == 0 {
		return nil
	}
	paths := Paths{Home: home, Root: c.Root}
	files := []string{paths.Src(c.Mise.Config)}
	for _, mc := range c.layerMise {
		files = append(files, mc.Path)
	}
	var errs []error
	pinned := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("mise config: %w", err))
			continue
		}
		var parsed struct {
			Tools map[string]any `toml:"tools"`
		}
		if err := toml.Unmarshal(data, &parsed); err != nil {
			errs = append(errs, fmt.Errorf("mise config %s is not valid TOML: %w", file, err))
			continue
		}
		for _, tool := range slices.Sorted(maps.Keys(parsed.Tools)) {
			if first, dup := pinned[tool]; dup {
				errs = append(errs, fmt.Errorf("mise tool %s is pinned in both %s and %s; mise would pick one", tool, first, file))
				continue
			}
			pinned[tool] = file
		}
	}
	return errs
}

// where names entry i of a section: by position in its file, and by the file
// too when layers are merged.
func (c *Config) where(section string, i int) string {
	if labels := c.origins[section]; i < len(labels) {
		return labels[i]
	}
	return fmt.Sprintf("%s[%d]", section, i)
}
