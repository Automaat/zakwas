package proposal

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func seqItem(text string) func(string) []string {
	return func(indent string) []string { return []string{indent + "- " + text} }
}

func TestYAMLAdd(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path []string
		kind yaml.Kind
		item func(string) []string
		want string
	}{
		{
			name: "appends after the last entry and keeps comments below",
			in:   "agents:\n  plugins:\n    - a@m\n    - b@m # note\n    # trailing\n  prune: true\n",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("c@m"),
			want: "agents:\n  plugins:\n    - a@m\n    - b@m # note\n    - c@m\n    # trailing\n  prune: true\n",
		},
		{
			name: "indentless sequence",
			in:   "agents:\n  plugins:\n  - a@m\nbrew:\n  file: Brewfile\n",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("b@m"),
			want: "agents:\n  plugins:\n  - a@m\n  - b@m\nbrew:\n  file: Brewfile\n",
		},
		{
			name: "creates missing keys at the end of the parent",
			in:   "# my config\nagents:\n    providers: [claude]\n\nbrew:\n  file: Brewfile\n",
			path: []string{"agents", "permissions", "deny"}, kind: yaml.SequenceNode, item: seqItem("rm"),
			want: "# my config\nagents:\n    providers: [claude]\n    permissions:\n      deny:\n        - rm\n\nbrew:\n  file: Brewfile\n",
		},
		{
			name: "null value becomes a list",
			in:   "agents:\n  plugins:\n  # none yet\n  prune: true\n",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("a@m"),
			want: "agents:\n  plugins:\n    - a@m\n  # none yet\n  prune: true\n",
		},
		{
			name: "creates the section in an empty file",
			in:   "",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("a@m"),
			want: "agents:\n  plugins:\n    - a@m\n",
		},
		{
			name: "file without a final newline keeps ending without one",
			in:   "agents:\n  plugins:\n    - a@m",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("b@m"),
			want: "agents:\n  plugins:\n    - a@m\n    - b@m",
		},
		{
			name: "CRLF files stay CRLF",
			in:   "agents:\r\n  plugins:\r\n    - a@m\r\nbrew:\r\n  file: B\r\n",
			path: []string{"agents", "plugins"}, kind: yaml.SequenceNode, item: seqItem("b@m"),
			want: "agents:\r\n  plugins:\r\n    - a@m\r\n    - b@m\r\nbrew:\r\n  file: B\r\n",
		},
		{
			name: "entry ending in a folded scalar is kept whole",
			in:   "agents:\n  permissions:\n    deny:\n      - command: rm\n        justification: >-\n          never delete\n\n          anything\n# end\n",
			path: []string{"agents", "permissions", "deny"}, kind: yaml.SequenceNode, item: seqItem("dd"),
			want: "agents:\n  permissions:\n    deny:\n      - command: rm\n        justification: >-\n          never delete\n\n          anything\n      - dd\n# end\n",
		},
		{
			name: "adds a mapping entry",
			in:   "agents:\n  mcp:\n    docs:\n      command: npx\n  prune: true\n",
			path: []string{"agents", "mcp"}, kind: yaml.MappingNode,
			item: func(i string) []string { return []string{i + "search:", i + "  url: https://x"} },
			want: "agents:\n  mcp:\n    docs:\n      command: npx\n    search:\n      url: https://x\n  prune: true\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			y, err := parseYAML([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if err := y.add(tt.path, tt.kind, tt.item); err != nil {
				t.Fatal(err)
			}
			if got := string(y.bytes()); got != tt.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tt.want)
			}
			var out any
			if err := yaml.Unmarshal(y.bytes(), &out); err != nil {
				t.Fatalf("result is not YAML: %v", err)
			}
		})
	}
}

func TestYAMLAddRejectsFlowStyle(t *testing.T) {
	for _, in := range []string{
		"agents:\n  plugins: [a@m]\n",
		"agents: {plugins: []}\n",
		"agents:\n  plugins: []\n",
	} {
		y, err := parseYAML([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		err = y.add([]string{"agents", "plugins"}, yaml.SequenceNode, seqItem("b@m"))
		if err == nil || !strings.Contains(err.Error(), "flow style") {
			t.Errorf("%q: err = %v, want a flow style error", in, err)
		}
	}
}

func TestYAMLRemove(t *testing.T) {
	in := "agents:\n  permissions:\n    allow:\n      - git status\n      - command: git log\n        providers: [claude]\n        justification: >-\n          read only\n\n          really\n      # keep me\n      - ls\n    deny:\n      - rm\n"
	match := func(cmd string) func(*yaml.Node) bool {
		return func(n *yaml.Node) bool { return itemField(n, "command") == cmd }
	}
	tests := []struct {
		name, cmd, want string
		count           int
	}{
		{"scalar entry", "git status", "agents:\n  permissions:\n    allow:\n      - command: git log\n        providers: [claude]\n        justification: >-\n          read only\n\n          really\n      # keep me\n      - ls\n    deny:\n      - rm\n", 1},
		{"mapping entry with a folded scalar", "git log", "agents:\n  permissions:\n    allow:\n      - git status\n      # keep me\n      - ls\n    deny:\n      - rm\n", 1},
		{"last entry", "ls", "agents:\n  permissions:\n    allow:\n      - git status\n      - command: git log\n        providers: [claude]\n        justification: >-\n          read only\n\n          really\n      # keep me\n    deny:\n      - rm\n", 1},
		{"no match", "pwd", in, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			y, err := parseYAML([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			n, err := y.remove([]string{"agents", "permissions", "allow"}, match(tt.cmd))
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.count {
				t.Errorf("removed %d, want %d", n, tt.count)
			}
			if got := string(y.bytes()); got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestScalar(t *testing.T) {
	tests := []struct {
		in   string
		flow bool
		want string
	}{
		{"git status", false, "git status"},
		{"name@market", false, "name@market"},
		{"true", false, `"true"`},
		{"123", false, `"123"`},
		{"a: b", false, `"a: b"`},
		{"-y", true, `"-y"`},
		{"a,b", true, `"a,b"`},
		{"a,b", false, "a,b"},
		{"say \"hi\"", false, `say "hi"`},
		{"tab\there", false, `"tab\there"`},
		{"https://example.com/mcp", false, "https://example.com/mcp"},
	}
	for _, tt := range tests {
		if got := scalar(tt.in, tt.flow); got != tt.want {
			t.Errorf("scalar(%q, %v) = %s, want %s", tt.in, tt.flow, got, tt.want)
		}
		var m map[string]string
		if err := yaml.Unmarshal([]byte("k: "+scalar(tt.in, false)), &m); err != nil || m["k"] != tt.in {
			t.Errorf("scalar(%q) reads back as %q (%v)", tt.in, m["k"], err)
		}
	}
}

func TestParseYAMLRejectsMoreThanOneDocument(t *testing.T) {
	for _, in := range []string{"a: 1\n---\nb: 2\n", "a: 1\n---\n"} {
		if _, err := parseYAML([]byte(in)); err == nil || !strings.Contains(err.Error(), "more than one YAML document") {
			t.Errorf("%q: err = %v", in, err)
		}
	}
	if _, err := parseYAML([]byte("a: 1\n---\n")); err == nil {
		t.Log("a trailing empty document is accepted or rejected, either keeps the file safe")
	}
}

func TestParseYAMLRejectsADocumentEndMarker(t *testing.T) {
	if _, err := parseYAML([]byte("agents:\n  plugins:\n    - p1@mkt\n...\n")); err == nil || !strings.Contains(err.Error(), "document end marker") {
		t.Errorf("err = %v", err)
	}
}
