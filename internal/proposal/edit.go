package proposal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/brew"
)

// Edit is one file an operation list changes. Rel is its path relative to
// the config repo, with forward slashes.
type Edit struct {
	Path string
	Rel  string
	Old  []byte
	New  []byte
}

// Result is the outcome of applying operations to the files on disk, which
// stay untouched: the new contents, and the config they parse to.
type Result struct {
	Edits  []Edit
	Config *config.Config
}

type editor struct {
	cfg   *config.Config
	home  string
	files map[string]*Edit
	order []string
}

// Apply runs the operations against the config repo's files in memory. The
// edited zakwas.yaml must still validate; an edit that doesn't is rejected.
func Apply(ops []Op, cfg *config.Config, home string) (*Result, error) {
	if len(ops) == 0 {
		return nil, errors.New("no operations")
	}
	e := &editor{cfg: cfg, home: home, files: map[string]*Edit{}}
	for i, op := range ops {
		if err := op.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", op.Summary(), err)
		}
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
	newCfg, err := config.Parse(e.files[cfg.Path].New, cfg.Path, home)
	if err != nil {
		return nil, fmt.Errorf("the edited config is invalid: %w", err)
	}
	res.Config = newCfg
	return res, nil
}

func (e *editor) file(path string) (*Edit, error) {
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
	rel, err := filepath.Rel(e.cfg.Root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s is outside the config repo %s, so an edit couldn't be committed", path, e.cfg.Root)
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
	cfgFile, err := e.file(e.cfg.Path)
	if err != nil {
		return err
	}
	paths := config.Paths{Home: e.home, Root: e.cfg.Root}
	switch op.Kind {
	case AddBrew:
		if e.cfg.Brew == nil {
			return errors.New("zakwas.yaml has no brew section")
		}
		f, err := e.file(paths.Src(e.cfg.Brew.File))
		if err != nil {
			return err
		}
		f.New, err = addBrew(f.New, op.Name, op.Cask)
		return err
	case PinMise:
		if e.cfg.Mise == nil {
			return errors.New("zakwas.yaml has no mise section")
		}
		f, err := e.file(paths.Src(e.cfg.Mise.Config))
		if err != nil {
			return err
		}
		f.New, err = pinMise(f.New, op.Name, op.Version)
		return err
	}
	y, err := parseYAML(cfgFile.New)
	if err != nil {
		return fmt.Errorf("%s: %w", e.cfg.Path, err)
	}
	if err := e.editYAML(y, op); err != nil {
		return err
	}
	cfgFile.New = y.bytes()
	return nil
}

func (e *editor) editYAML(y *yamlFile, op Op) error {
	switch op.Kind {
	case AddPlugin:
		_, mkt, _ := strings.Cut(op.Name, "@")
		if e.cfg.Agents == nil || !hasKey(e.cfg.Agents.Marketplaces, mkt) {
			return fmt.Errorf("marketplace %q is not declared under agents.marketplaces", mkt)
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
	if len(op.Providers) > 0 {
		lines = append(lines, child+"providers: "+flowList(op.Providers))
	}
	return lines
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

func addBrew(data []byte, name string, cask bool) ([]byte, error) {
	kind := "brew"
	if cask {
		kind = "cask"
	}
	for _, en := range brew.ParseEntries(data) {
		if en.Kind == kind && en.Name == name {
			return nil, fmt.Errorf("%s %q is already in the Brewfile", kind, name)
		}
	}
	for line := range strings.Lines(string(data)) {
		if g := singleQuoted.FindStringSubmatch(strings.TrimSpace(line)); g != nil && g[1] == kind && g[2] == name {
			return nil, fmt.Errorf("%s %q is already in the Brewfile", kind, name)
		}
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
