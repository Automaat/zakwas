package proposal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/brew"
	"github.com/Automaat/zakwas/internal/platform"
)

// Edit is one file an operation list changes. Rel is its path relative to
// the root of the repo it belongs to, with forward slashes.
type Edit struct {
	Path string
	Rel  string
	Old  []byte
	New  []byte
}

// Result is the outcome of applying operations to the files on disk, which
// stay untouched: the new contents, and the config they parse to. Layer
// names the local layer whose repo holds every edit, empty for the main
// config's; Root is that repo's directory.
type Result struct {
	Edits  []Edit
	Config *config.Config
	Layer  string
	Root   string
}

type editor struct {
	cfg   *config.Config
	home  string
	files map[string]*Edit
	order []string
	// markets are the marketplaces earlier operations declared, with the
	// layer each went to ("" for the main config), which later plugins may
	// come from.
	markets map[string]string
	// repo is the one repo the operations edit, once the first has chosen it.
	repo *target
}

// target is where an operation's edits go: the main config's repo, or the
// repo of a local layer.
type target struct {
	layer string
	root  string
	yaml  string
}

func (t target) String() string {
	if t.layer == "" {
		return "the main config"
	}
	return fmt.Sprintf("layer %q", t.layer)
}

// Apply runs the operations against the config repo's files in memory. The
// edited zakwas.yaml must still validate; an edit that doesn't is rejected.
func Apply(ops []Op, cfg *config.Config, home string) (*Result, error) {
	if err := ValidateOps(ops); err != nil {
		return nil, err
	}
	e := &editor{cfg: cfg, home: home, files: map[string]*Edit{}, markets: map[string]string{}}
	for i, op := range ops {
		if err := e.apply(op); err != nil {
			return nil, fmt.Errorf("operation %d (%s): %w", i+1, op.Summary(), err)
		}
	}
	res := &Result{}
	for _, p := range e.order {
		if f := e.files[p]; !bytes.Equal(f.Old, f.New) {
			res.Edits = append(res.Edits, *f)
		}
	}
	if len(res.Edits) == 0 {
		return nil, errors.New("the operations change nothing")
	}
	sources := map[string][]byte{}
	for _, l := range cfg.Layers {
		if f, ok := e.files[l.Path]; ok {
			sources[l.Path] = f.New
		}
	}
	newCfg, err := config.ParseLayeredWith(e.files[cfg.Path].New, cfg.Path, home, cfg.GOOS, cfg.LayerRefs(), sources)
	if err != nil {
		return nil, fmt.Errorf("the edited config is invalid: %w", err)
	}
	res.Config = newCfg
	res.Layer, res.Root = e.repo.layer, e.repo.root
	return res, nil
}

// main is the target of the main config's repo.
func (e *editor) main() target { return target{root: e.cfg.Root, yaml: e.cfg.Path} }

// layerNamed is the target of a registered layer's repo.
func (e *editor) layerNamed(name string) (target, error) {
	for _, l := range e.cfg.Layers {
		if l.Name == name {
			return target{layer: name, root: l.Root, yaml: l.Path}, nil
		}
	}
	return target{}, fmt.Errorf("layer %q is not registered for %s; list the layers with `zakwas layer list`", name, e.cfg.Path)
}

// choose records the repo an operation edits. A proposal is committed in one
// repo, so every operation must edit the same one.
func (e *editor) choose(t target) error {
	if e.repo == nil {
		e.repo = &t
		return nil
	}
	if *e.repo != t {
		return fmt.Errorf("it edits %s, but earlier operations edit %s; a proposal is committed in one repo, so propose them separately", t, e.repo)
	}
	return nil
}

// addTarget is the repo an operation that adds an entry edits: the layer it
// names, else the main config's.
func (e *editor) addTarget(op Op) (target, error) {
	if op.Layer == "" {
		return e.main(), nil
	}
	return e.layerNamed(op.Layer)
}

// holderTarget is the repo of the file that holds an entry. An operation
// that names a layer must name the one that holds it.
func (e *editor) holderTarget(op Op, file string) (target, error) {
	holder := e.main()
	for _, l := range e.cfg.Layers {
		if l.Path == file {
			holder = target{layer: l.Name, root: l.Root, yaml: l.Path}
		}
	}
	if op.Layer != "" && op.Layer != holder.layer {
		return target{}, fmt.Errorf("the entry is in %s (%s), not in layer %q", holder, file, op.Layer)
	}
	return holder, nil
}

func (e *editor) file(path, root string) (*Edit, error) {
	if f, ok := e.files[path]; ok {
		return f, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink; edit the file it points to by hand", path)
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s is outside the config repo %s, so an edit couldn't be committed", path, root)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &Edit{Path: path, Rel: filepath.ToSlash(rel), Old: data, New: slices.Clone(data)}
	e.files[path] = f
	e.order = append(e.order, path)
	return f, nil
}

func (e *editor) apply(op Op) error {
	// The main zakwas.yaml is always read: the edited config is parsed from it.
	if _, err := e.file(e.cfg.Path, e.cfg.Root); err != nil {
		return err
	}
	paths := config.Paths{Home: e.home, Root: e.cfg.Root}
	switch op.Kind {
	case AddBrew:
		return e.brewOp(op, paths)
	case PinMise:
		return e.miseOp(op, paths)
	}
	t, err := e.yamlTarget(op)
	if err != nil {
		return err
	}
	if err := e.choose(t); err != nil {
		return err
	}
	f, err := e.file(t.yaml, t.root)
	if err != nil {
		return err
	}
	y, err := parseYAML(f.New)
	if err != nil {
		return fmt.Errorf("%s: %w", t.yaml, err)
	}
	if err := e.editYAML(y, op, t); err != nil {
		return err
	}
	f.New = y.bytes()
	return nil
}

// yamlTarget is the repo of the zakwas.yaml an operation edits.
func (e *editor) yamlTarget(op Op) (target, error) {
	if op.Kind != RemovePermission {
		return e.addTarget(op)
	}
	if op.Layer != "" {
		if _, err := e.layerNamed(op.Layer); err != nil {
			return target{}, err
		}
	}
	want := normalize(op.Command)
	var files []string
	for i, rule := range e.rules(op.Decision) {
		if normalize(rule.Command) != want {
			continue
		}
		file := e.cfg.Agents.EntryFile("agents.permissions."+op.Decision, i)
		if file == "" {
			file = e.cfg.Path
		}
		if !slices.Contains(files, file) {
			files = append(files, file)
		}
	}
	switch len(files) {
	case 0:
		// Not declared anywhere: editing reports it.
		return e.addTarget(op)
	case 1:
		return e.holderTarget(op, files[0])
	}
	return target{}, fmt.Errorf("%s has a rule for %q in %s; remove it with the layer that holds it named", op.Decision, want, strings.Join(files, " and "))
}

// rules are the merged permission rules of one decision.
func (e *editor) rules(decision string) []config.AgentPermission {
	if e.cfg.Agents == nil || e.cfg.Agents.Permissions == nil {
		return nil
	}
	p := e.cfg.Agents.Permissions
	switch decision {
	case config.PermissionAllow:
		return p.Allow
	case config.PermissionReview:
		return p.Review
	}
	return p.Deny
}

func (e *editor) brewOp(op Op, paths config.Paths) error {
	if err := platform.ModuleError("brew", e.cfg.OS()); err != nil {
		return fmt.Errorf("add-brew: %w", err)
	}
	if e.cfg.Brew == nil {
		return errors.New("zakwas.yaml has no brew section")
	}
	t, err := e.addTarget(op)
	if err != nil {
		return err
	}
	file := paths.Src(e.cfg.Brew.File)
	if t.layer != "" {
		file = ""
		for _, lb := range e.cfg.Brew.Layers {
			if lb.Layer == t.layer {
				file = lb.File
			}
		}
		if file == "" {
			return fmt.Errorf("layer %q has no brew.file; set it in %s first", t.layer, t.yaml)
		}
	}
	if err := e.choose(t); err != nil {
		return err
	}
	kind := "brew"
	if op.Cask {
		kind = "cask"
	}
	for _, other := range e.brewfiles(paths) {
		if other == file {
			continue
		}
		data, err := os.ReadFile(other)
		if err != nil {
			return err
		}
		if hasBrewEntry(data, kind, op.Name) {
			return fmt.Errorf("%s %q is already in the Brewfile %s", kind, op.Name, other)
		}
	}
	f, err := e.file(file, t.root)
	if err != nil {
		return err
	}
	f.New, err = addBrew(f.New, op.Name, op.Cask)
	return err
}

// brewfiles lists the Brewfiles of the config and its layers.
func (e *editor) brewfiles(paths config.Paths) []string {
	out := []string{paths.Src(e.cfg.Brew.File)}
	for _, lb := range e.cfg.Brew.Layers {
		out = append(out, lb.File)
	}
	return out
}

func (e *editor) miseOp(op Op, paths config.Paths) error {
	if err := platform.ModuleError("mise", e.cfg.OS()); err != nil {
		return fmt.Errorf("pin-mise: %w", err)
	}
	if e.cfg.Mise == nil {
		return errors.New("zakwas.yaml has no mise section")
	}
	pins, err := e.cfg.MisePins(op.Name, e.home)
	if err != nil {
		return fmt.Errorf("pin-mise: %w", err)
	}
	var (
		t    target
		file string
	)
	switch {
	case len(pins) > 0:
		// Bumping a pin edits the file that holds it.
		pin := pins[0]
		file = pin.Path
		if t, err = e.pinHolder(op, pin); err != nil {
			return fmt.Errorf("pin-mise: %w", err)
		}
	case op.Layer == "":
		t, file = e.main(), paths.Src(e.cfg.Mise.Config)
	default:
		if t, err = e.layerNamed(op.Layer); err != nil {
			return err
		}
		for _, mc := range e.cfg.LayerMise() {
			if mc.Layer == t.layer {
				file = mc.Path
			}
		}
		if file == "" {
			return fmt.Errorf("pin-mise: layer %q has no mise.config; set it in %s first", t.layer, t.yaml)
		}
	}
	if err := e.choose(t); err != nil {
		return err
	}
	f, err := e.file(file, t.root)
	if err != nil {
		return err
	}
	f.New, err = pinMise(f.New, op.Name, op.Version)
	return err
}

// pinHolder is the repo of the mise config that pins a tool already. A
// layer named for a tool the main config pins, or another layer, is refused:
// a second pin would make mise pick one of two.
func (e *editor) pinHolder(op Op, pin config.MisePin) (target, error) {
	holder := e.main()
	if pin.Layer != "" {
		var err error
		if holder, err = e.layerNamed(pin.Layer); err != nil {
			return target{}, err
		}
	}
	if op.Layer != "" && op.Layer != pin.Layer {
		return target{}, fmt.Errorf("%s is pinned in %s (%s), not in layer %q; mise would pick one of two pins", op.Name, holder, pin.Path, op.Layer)
	}
	return holder, nil
}

func (e *editor) editYAML(y *yamlFile, op Op, t target) error {
	switch op.Kind {
	case AddMarketplace:
		if e.cfg.Agents != nil && hasKey(e.cfg.Agents.Marketplaces, op.Name) {
			return fmt.Errorf("marketplace %s is already declared", op.Name)
		}
		if _, declared := e.markets[op.Name]; declared {
			return fmt.Errorf("marketplace %s is already declared", op.Name)
		}
		e.markets[op.Name] = t.layer
		return y.add([]string{"agents", "marketplaces"}, yaml.MappingNode, func(indent string) []string {
			lines := []string{indent + keyText(op.Name) + ":", indent + "  source: " + scalar(op.Source, false)}
			if len(op.Providers) > 0 {
				lines = append(lines, indent+"  providers: "+flowList(op.Providers))
			}
			return lines
		})
	case AddPlugin:
		_, mkt, _ := strings.Cut(op.Name, "@")
		if err := e.marketplaceFor(mkt, t); err != nil {
			return err
		}
		path := []string{"agents", "plugins"}
		if len(itemsWhere(y, path, func(n *yaml.Node) bool { return itemField(n, "id") == op.Name })) > 0 {
			return fmt.Errorf("plugin %s is already declared", op.Name)
		}
		return y.add(path, yaml.SequenceNode, func(indent string) []string {
			if len(op.Providers) == 0 {
				return []string{indent + "- " + scalar(op.Name, false)}
			}
			return []string{
				indent + "- id: " + scalar(op.Name, false),
				indent + "  providers: " + flowList(op.Providers),
			}
		})
	case AddPermission:
		path := []string{"agents", "permissions", op.Decision}
		want := normalize(op.Command)
		if len(itemsWhere(y, path, func(n *yaml.Node) bool { return normalize(itemField(n, "command")) == want })) > 0 {
			return fmt.Errorf("%s already has a rule for %q", op.Decision, want)
		}
		return y.add(path, yaml.SequenceNode, func(indent string) []string {
			if len(op.Providers) == 0 && op.Justification == "" {
				return []string{indent + "- " + scalar(want, false)}
			}
			lines := []string{indent + "- command: " + scalar(want, false)}
			if len(op.Providers) > 0 {
				lines = append(lines, indent+"  providers: "+flowList(op.Providers))
			}
			if op.Justification != "" {
				lines = append(lines, indent+"  justification: "+scalar(op.Justification, false))
			}
			return lines
		})
	case RemovePermission:
		want := normalize(op.Command)
		n, err := y.remove([]string{"agents", "permissions", op.Decision}, func(item *yaml.Node) bool {
			return normalize(itemField(item, "command")) == want
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%s has no rule for %q", op.Decision, want)
		}
		return nil
	case AddMCP:
		if e.cfg.Agents != nil && hasKey(e.cfg.Agents.MCP, op.Name) {
			return fmt.Errorf("MCP server %s is already declared", op.Name)
		}
		return y.add([]string{"agents", "mcp"}, yaml.MappingNode, func(indent string) []string {
			return mcpEntry(indent, op)
		})
	}
	return fmt.Errorf("unsupported operation %q", op.Kind)
}

// marketplaceFor checks a plugin's marketplace is declared where the plugin
// can use it. The main config can't lean on a marketplace only a layer
// declares: the repo would stop validating on a machine without the layer.
func (e *editor) marketplaceFor(mkt string, t target) error {
	var declaredIn string
	switch {
	case e.cfg.Agents != nil && hasKey(e.cfg.Agents.Marketplaces, mkt):
		if file := e.cfg.Agents.KeyedFile("marketplaces", mkt); file != "" {
			for _, l := range e.cfg.Layers {
				if l.Path == file {
					declaredIn = l.Name
				}
			}
		}
	default:
		layer, ok := e.markets[mkt]
		if !ok {
			return fmt.Errorf("marketplace %q is not declared under agents.marketplaces", mkt)
		}
		declaredIn = layer
	}
	if t.layer == "" && declaredIn != "" {
		return fmt.Errorf("marketplace %q is declared only by layer %q, and the main config can't depend on a layer; add the plugin to that layer", mkt, declaredIn)
	}
	return nil
}

func mcpEntry(indent string, op Op) []string {
	s := op.Server
	child := indent + "  "
	lines := []string{indent + keyText(op.Name) + ":"}
	if s.Command != "" {
		lines = append(lines, child+"command: "+scalar(s.Command, false))
	}
	if len(s.Args) > 0 {
		lines = append(lines, child+"args: "+flowList(s.Args))
	}
	if len(s.EnvVars) > 0 {
		lines = append(lines, child+"envVars: "+flowList(s.EnvVars))
	}
	if s.URL != "" {
		lines = append(lines, child+"url: "+scalar(s.URL, false))
	}
	if s.BearerTokenEnv != "" {
		lines = append(lines, child+"bearerTokenEnv: "+scalar(s.BearerTokenEnv, false))
	}
	if s.OAuth != nil {
		lines = append(lines, child+"oauth: "+oauthFlow(s.OAuth))
	}
	if len(op.Providers) > 0 {
		lines = append(lines, child+"providers: "+flowList(op.Providers))
	}
	return lines
}

func oauthFlow(o *OAuth) string {
	parts := []string{"clientId: " + scalar(o.ClientID, true)}
	if o.CallbackPort != nil {
		parts = append(parts, "callbackPort: "+strconv.Itoa(*o.CallbackPort))
	}
	if o.Scope != "" {
		parts = append(parts, "scope: "+scalar(o.Scope, true))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func hasKey[V any](m map[string]V, key string) bool {
	_, ok := m[key]
	return ok
}

func normalize(command string) string { return strings.Join(strings.Fields(command), " ") }

func itemsWhere(y *yamlFile, path []string, match func(*yaml.Node) bool) []*yaml.Node {
	loc, err := y.locate(path)
	if err != nil || loc.missing != nil || loc.node == nil || loc.node.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, item := range loc.node.Content {
		if match(item) {
			out = append(out, item)
		}
	}
	return out
}

// itemField is a sequence entry's value: the scalar itself, or the named
// field of a mapping entry.
func itemField(n *yaml.Node, field string) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.MappingNode:
		if _, v := mapGet(n, field); v != nil && v.Kind == yaml.ScalarNode {
			return v.Value
		}
	}
	return ""
}

// hasBrewEntry reports whether the Brewfile declares the package, in either
// quote style.
func hasBrewEntry(data []byte, kind, name string) bool {
	for _, en := range brew.ParseEntries(data) {
		if en.Kind == kind && en.Name == name {
			return true
		}
	}
	for line := range strings.Lines(string(data)) {
		if g := singleQuoted.FindStringSubmatch(strings.TrimSpace(line)); g != nil && g[1] == kind && g[2] == name {
			return true
		}
	}
	return false
}

func addBrew(data []byte, name string, cask bool) ([]byte, error) {
	kind := "brew"
	if cask {
		kind = "cask"
	}
	if hasBrewEntry(data, kind, name) {
		return nil, fmt.Errorf("%s %q is already in the Brewfile", kind, name)
	}
	if err := plainBrewfile(data); err != nil {
		return nil, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	at := len(lines)
	for i := range lines {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), kind+" ") || strings.HasPrefix(strings.TrimSpace(lines[i]), kind+"\t") {
			at = i + 1
			for at < len(lines) && continued(lines[at-1]) {
				at++
			}
		}
	}
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	atEnd := at == len(lines)
	if atEnd && len(lines) > 0 && !strings.HasSuffix(lines[at-1], "\n") {
		lines[at-1] += eol
	}
	lines = slices.Insert(lines, at, kind+` "`+name+`"`+eol)
	out := strings.Join(lines, "")
	if atEnd && len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		out = strings.TrimSuffix(out, eol)
	}
	return []byte(out), nil
}

var (
	singleQuoted = regexp.MustCompile(`^(tap|brew|cask)\s+'([^']+)'`)
	rubyBlock    = regexp.MustCompile(`^\s*(if|unless|else|elsif|end|case|when|begin|while|until|for)\b|\bdo(\s*\|[^|]*\|)?\s*$`)
	quotedString = regexp.MustCompile(`"[^"]*"|'[^']*'`)
)

// plainBrewfile refuses a Brewfile an inserted line could land inside: one
// with Ruby blocks or an entry that spans lines through an open bracket.
func plainBrewfile(data []byte) error {
	depth := 0
	for line := range strings.Lines(string(data)) {
		code := strings.TrimSpace(quotedString.ReplaceAllString(line, `""`))
		if i := strings.Index(code, "#"); i >= 0 {
			code = strings.TrimSpace(code[:i])
		}
		if rubyBlock.MatchString(code) {
			return errors.New("the Brewfile has Ruby blocks; add the package by hand")
		}
		depth += strings.Count(code, "[") + strings.Count(code, "(") + strings.Count(code, "{")
		depth -= strings.Count(code, "]") + strings.Count(code, ")") + strings.Count(code, "}")
		if depth != 0 {
			return errors.New("the Brewfile has an entry that spans lines; add the package by hand")
		}
	}
	return nil
}

// continued reports whether a Brewfile line continues on the next one.
func continued(line string) bool {
	code := strings.TrimSpace(line)
	if i := strings.Index(code, " #"); i >= 0 && !strings.Contains(code[:i], `"`) {
		code = strings.TrimSpace(code[:i])
	}
	return strings.HasSuffix(code, ",") || strings.HasSuffix(code, `\`)
}

func pinMise(data []byte, tool, version string) ([]byte, error) {
	out, err := editMiseTools(data, tool, version)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("the edited mise config is not valid TOML: %w", err)
	}
	tools, _ := doc["tools"].(map[string]any)
	if got, _ := tools[tool].(string); got != version {
		return nil, fmt.Errorf("couldn't pin %s in the [tools] table; edit the mise config by hand", tool)
	}
	return out, nil
}
