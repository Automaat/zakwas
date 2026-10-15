package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func TestTOMLHeaders(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want []string
	}{
		{"plain", "[a]\nx = 1\n[b.c]\n", []string{"a", "b|c"}},
		{"array table", "[[a.b]]\n", []string{"a|b"}},
		{"quoted keys", "[ \"a.b\" . 'c d' ]  # note\n", []string{"a.b|c d"}},
		{"indented", "  \t[a]\n", []string{"a"}},
		{"multi-line basic string", "x = \"\"\"\n[fake]\n\"\"\"\n[real]\n", []string{"real"}},
		{"multi-line literal string", "x = '''\n[fake]\n'''\n[real]\n", []string{"real"}},
		{"escaped quote in multi-line string", "x = \"\"\"\n\\\"\"\"\n[fake]\n\"\"\"\n[real]\n", []string{"real"}},
		{"multi-line array", "x = [\n  \"a\",\n[1, 2],\n]\n[real]\n", []string{"real"}},
		{"multi-line inline table", "x = {\n[fake]\n}\n[real]\n", []string{"real"}},
		{"brackets in strings", "x = \"[\" # [\n[real]\n", []string{"real"}},
		{"literal string with quote", "x = '\"'\n[real]\n", []string{"real"}},
		{"string closing on one line", "x = \"\"\"a\"\"\"\n[real]\n", []string{"real"}},
		{"comment", "# [fake]\n[real]\n", []string{"real"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, h := range tomlHeaders(splitLines([]byte(tt.toml))) {
				got = append(got, strings.Join(h.path, "|"))
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("headers = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEditServer(t *testing.T) {
	const text = "[mcp_servers.a]\ncommand = \"new\"\n"
	tests := []struct {
		name, in, replaced, removed string
	}{
		{
			name:     "middle",
			in:       "x = 1\n\n[mcp_servers.a]\ncommand = \"old\"\n\n# for b\n[b]\ny = 2\n",
			replaced: "x = 1\n\n[mcp_servers.a]\ncommand = \"new\"\n\n# for b\n[b]\ny = 2\n",
			removed:  "x = 1\n\n# for b\n[b]\ny = 2\n",
		},
		{
			name:     "last",
			in:       "x = 1\n\n[mcp_servers.a]\ncommand = \"old\"\n",
			replaced: "x = 1\n\n[mcp_servers.a]\ncommand = \"new\"\n",
			removed:  "x = 1\n",
		},
		{
			name:     "only",
			in:       "[mcp_servers.a]\ncommand = \"old\"\n",
			replaced: "[mcp_servers.a]\ncommand = \"new\"\n",
			removed:  "",
		},
		{
			name:     "subtables apart",
			in:       "[mcp_servers.a]\ncommand = \"old\"\n\n[other]\nz = 1\n\n[mcp_servers.a.env]\nK = \"v\"\n",
			replaced: "[mcp_servers.a]\ncommand = \"new\"\n\n[other]\nz = 1\n",
			removed:  "[other]\nz = 1\n",
		},
		{
			name:     "neighbour with a longer name",
			in:       "[mcp_servers.ab]\ncommand = \"x\"\n\n[mcp_servers.a]\ncommand = \"old\"\n",
			replaced: "[mcp_servers.ab]\ncommand = \"x\"\n\n[mcp_servers.a]\ncommand = \"new\"\n",
			removed:  "[mcp_servers.ab]\ncommand = \"x\"\n",
		},
		{
			name:     "trailing comment at the end of file",
			in:       "[mcp_servers.a]\ncommand = \"old\"\n# end\n",
			replaced: "[mcp_servers.a]\ncommand = \"new\"\n# end\n",
			removed:  "# end\n",
		},
		{
			name:     "quoted name",
			in:       "[mcp_servers.\"a\"]\ncommand = \"old\"\n",
			replaced: "[mcp_servers.a]\ncommand = \"new\"\n",
			removed:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := replaceServer([]byte(tt.in), "a", text)
			if !ok || string(got) != tt.replaced {
				t.Errorf("replace = %q (%v), want %q", got, ok, tt.replaced)
			}
			got, ok = removeServer([]byte(tt.in), "a")
			if !ok || string(got) != tt.removed {
				t.Errorf("remove = %q (%v), want %q", got, ok, tt.removed)
			}
		})
	}
	if _, ok := replaceServer([]byte("[b]\n"), "a", text); ok {
		t.Error("replaced a server that is not there")
	}
	if _, ok := removeServer([]byte("[mcp_servers]\na = {}\n"), "a"); ok {
		t.Error("removed an inline server")
	}
}

func TestAppendServer(t *testing.T) {
	for in, want := range map[string]string{
		"":          "[t]\n",
		"x = 1":     "x = 1\n\n[t]\n",
		"x = 1\n":   "x = 1\n\n[t]\n",
		"x = 1\n\n": "x = 1\n\n[t]\n",
	} {
		if got := string(appendServer([]byte(in), "[t]\n")); got != want {
			t.Errorf("append to %q = %q, want %q", in, got, want)
		}
	}
}

func TestRenderCodexMCPParsesBack(t *testing.T) {
	s := config.MCPServer{
		Command: "run \"x\"", Args: []string{"a\\b", "tab\there", "ünï", "q'uote"},
		Env: map[string]string{"A.B": "line\nbreak", "PLAIN": "v"}, EnvVars: []string{"TOKEN"},
	}
	entry, err := codexMCP{}.render("srv", s)
	if err != nil {
		t.Fatal(err)
	}
	if got := entry.value["command"]; got != "run \"x\"" {
		t.Errorf("command = %q", got)
	}
	args, _ := entry.value["args"].([]any)
	if len(args) != 4 || args[0] != "a\\b" || args[1] != "tab\there" || args[2] != "ünï" || args[3] != "q'uote" {
		t.Errorf("args = %q", args)
	}
	env, _ := entry.value["env"].(map[string]any)
	if env["A.B"] != "line\nbreak" || env["PLAIN"] != "v" {
		t.Errorf("env = %v", env)
	}
}
