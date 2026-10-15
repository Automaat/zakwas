package proposal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// yamlFile edits YAML as text. Re-encoding through the YAML library would
// rewrite unrelated lines and change folded scalars, so the parsed tree only
// tells where entries are; every edit splices whole lines of the original.
// Lines keep their own endings, and only the last may lack one. root is the
// top-level mapping, nil for an empty document.
type yamlFile struct {
	lines []string
	eol   string
	root  *yaml.Node
}

func parseYAML(data []byte) (*yamlFile, error) {
	f := &yamlFile{eol: "\n"}
	if bytes.Count(data, []byte("\r\n"))*2 > bytes.Count(data, []byte("\n")) {
		f.eol = "\r\n"
	}
	f.lines = strings.SplitAfter(string(data), "\n")
	if f.lines[len(f.lines)-1] == "" {
		f.lines = f.lines[:len(f.lines)-1]
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) > 0 {
		root := doc.Content[0]
		switch {
		case root.Kind == yaml.MappingNode && isBlock(root):
			f.root = root
		case isNull(root):
		default:
			return nil, errors.New("the top level must be a block mapping")
		}
	}
	return f, nil
}

func (f *yamlFile) bytes() []byte { return []byte(strings.Join(f.lines, "")) }

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func isBlock(n *yaml.Node) bool { return n.Style&yaml.FlowStyle == 0 }

func mapGet(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// location is where a key path ends: node is the value at the path, or the
// deepest mapping reached when keys are missing; key is the key node that
// holds node.
type location struct {
	node    *yaml.Node
	key     *yaml.Node
	missing []string
}

func (f *yamlFile) locate(path []string) (location, error) {
	cur := f.root
	var curKey *yaml.Node
	for i, k := range path {
		switch {
		case cur == nil, isNull(cur):
			return location{node: cur, key: curKey, missing: path[i:]}, nil
		case cur.Kind != yaml.MappingNode:
			return location{}, fmt.Errorf("%s is not a mapping", strings.Join(path[:i], "."))
		case !isBlock(cur):
			return location{}, flowError(path[:i])
		}
		kn, vn := mapGet(cur, k)
		if vn == nil {
			return location{node: cur, key: curKey, missing: path[i:]}, nil
		}
		cur, curKey = vn, kn
	}
	return location{node: cur, key: curKey}, nil
}

func flowError(path []string) error {
	return fmt.Errorf("%s is written in flow style, which edits don't support; rewrite it as a block", strings.Join(path, "."))
}

func indentOf(line string) int { return len(line) - len(strings.TrimLeft(line, " ")) }

func blank(line string) bool { return strings.TrimSpace(line) == "" }

func comment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func (f *yamlFile) dashLine(item *yaml.Node) bool {
	l := item.Line - 1
	return l >= 0 && l < len(f.lines) && strings.HasPrefix(strings.TrimSpace(f.lines[l]), "-")
}

// extent is the index of the last line n occupies. Comments and blank lines
// after its last value belong to what follows, except inside a block scalar,
// where they are content.
func (f *yamlFile) extent(n *yaml.Node) int {
	last, lastNode, lastIndent := n.Line-1, n, 0
	var walk func(n *yaml.Node, parentIndent int)
	walk = func(n *yaml.Node, parentIndent int) {
		if n.Kind == yaml.ScalarNode && n.Line-1 >= last {
			last, lastNode, lastIndent = n.Line-1, n, parentIndent
		}
		for i, c := range n.Content {
			switch {
			case n.Kind == yaml.MappingNode && i%2 == 0:
				walk(c, c.Column-1)
			case n.Kind == yaml.MappingNode:
				walk(c, n.Content[i-1].Column-1)
			case f.dashLine(c):
				walk(c, indentOf(f.lines[c.Line-1]))
			default:
				walk(c, parentIndent)
			}
		}
	}
	walk(n, 0)
	next := len(f.lines)
	var visit func(m *yaml.Node)
	visit = func(m *yaml.Node) {
		if l := m.Line - 1; l > last && l < next {
			next = l
		}
		for _, c := range m.Content {
			visit(c)
		}
	}
	if f.root != nil {
		visit(f.root)
	}
	end := last
	switch {
	case lastNode.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		for j := last + 1; j < next; j++ {
			if blank(f.lines[j]) {
				continue
			}
			if indentOf(f.lines[j]) <= lastIndent {
				break
			}
			end = j
		}
	case !isNull(lastNode):
		for j := last + 1; j < next; j++ {
			if !blank(f.lines[j]) && !comment(f.lines[j]) {
				end = j
			}
		}
	}
	return end
}

// insert puts lines before index at. Appending to a file without a final
// newline terminates the old last line and leaves the new last line open, so
// the file keeps ending the way it did.
func (f *yamlFile) insert(at int, lines []string) {
	at = min(at, len(f.lines))
	unterminated := at == len(f.lines) && at > 0 && !strings.HasSuffix(f.lines[at-1], "\n")
	out := slices.Clone(f.lines[:at])
	for _, l := range lines {
		out = append(out, l+f.eol)
	}
	out = append(out, f.lines[at:]...)
	if unterminated {
		out[at-1] += f.eol
		out[len(out)-1] = strings.TrimSuffix(out[len(out)-1], f.eol)
	}
	f.lines = out
}

// add inserts the lines build returns into the block collection at path of
// the given kind, creating the keys on the way. build gets the indentation
// of the collection's entries.
func (f *yamlFile) add(path []string, want yaml.Kind, build func(indent string) []string) error {
	loc, err := f.locate(path)
	if err != nil {
		return err
	}
	nested := func(indent int) []string {
		var out []string
		for _, k := range loc.missing {
			out = append(out, strings.Repeat(" ", indent)+keyText(k)+":")
			indent += 2
		}
		return append(out, build(strings.Repeat(" ", indent))...)
	}
	switch {
	case loc.node == nil:
		f.insert(len(f.lines), nested(0))
	case loc.missing != nil && loc.key == nil && isNull(loc.node):
		f.insert(len(f.lines), nested(0))
	case loc.missing != nil && isNull(loc.node):
		f.insert(f.extent(loc.key)+1, nested(loc.key.Column-1+2))
	case loc.missing != nil:
		f.insert(f.extent(loc.node)+1, nested(loc.node.Content[0].Column-1))
	case isNull(loc.node):
		f.insert(f.extent(loc.key)+1, nested(loc.key.Column-1+2))
	case loc.node.Kind != want:
		return fmt.Errorf("%s has the wrong type", strings.Join(path, "."))
	case !isBlock(loc.node) || len(loc.node.Content) == 0:
		return flowError(path)
	default:
		first := loc.node.Content[0]
		indent := first.Column - 1
		if want == yaml.SequenceNode {
			if !f.dashLine(first) {
				return fmt.Errorf("%s: can't find the dash of its first entry", strings.Join(path, "."))
			}
			indent = indentOf(f.lines[first.Line-1])
		}
		f.insert(f.extent(loc.node)+1, build(strings.Repeat(" ", indent)))
	}
	return nil
}

// remove deletes the entries of the block sequence at path that match, and
// returns how many it removed.
func (f *yamlFile) remove(path []string, match func(item *yaml.Node) bool) (int, error) {
	loc, err := f.locate(path)
	if err != nil {
		return 0, err
	}
	if loc.missing != nil || loc.node == nil || isNull(loc.node) {
		return 0, nil
	}
	seq := loc.node
	switch {
	case seq.Kind != yaml.SequenceNode:
		return 0, fmt.Errorf("%s has the wrong type", strings.Join(path, "."))
	case !isBlock(seq):
		return 0, flowError(path)
	}
	var drop [][2]int
	for _, item := range seq.Content {
		if !match(item) {
			continue
		}
		if !f.dashLine(item) {
			return 0, fmt.Errorf("%s: an entry doesn't start on its dash line", strings.Join(path, "."))
		}
		drop = append(drop, [2]int{item.Line - 1, f.extent(item)})
	}
	for _, r := range slices.Backward(drop) {
		f.lines = slices.Delete(f.lines, r[0], r[1]+1)
	}
	return len(drop), nil
}

func keyText(k string) string { return scalar(k, false) }

// scalar renders a string as YAML: plain when that reads back as the same
// string, double-quoted otherwise. In flow context it also avoids the
// characters that delimit a flow collection.
func scalar(s string, flow bool) string {
	if plainSafe(s, flow) {
		return s
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func plainSafe(s string, flow bool) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") {
		return false
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return false
	}
	if flow && strings.ContainsAny(s, ",[]{}") {
		return false
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 {
			return false
		}
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte("k: "+s), &got); err != nil {
		return false
	}
	v, ok := got["k"].(string)
	return ok && v == s
}

func flowList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = scalar(s, true)
	}
	return "[" + strings.Join(q, ", ") + "]"
}
