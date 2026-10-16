package config

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/testutil"
)

func TestExpandHome(t *testing.T) {
	for _, tt := range []struct {
		name, home, in, want string
	}{
		{"bare tilde", "/Users/me", "~", "/Users/me"},
		{"path", "/Users/me", "~/x", "/Users/me/x"},
		{"nested", "/Users/me", "~/.local/bin/x", "/Users/me/.local/bin/x"},
		{"trailing slash", "/Users/me", "~/", "/Users/me/"},
		{"rest kept as written", "/Users/me", "~/a/../b//c/", "/Users/me/a/../b//c/"},
		{"other user", "/Users/me", "~user/x", "~user/x"},
		{"tilde name", "/Users/me", "~x", "~x"},
		{"tilde in the middle", "/Users/me", "a~/x", "a~/x"},
		{"flag value", "/Users/me", "--root=~/x", "--root=~/x"},
		{"second tilde", "/Users/me", "~/~/x", "/Users/me/~/x"},
		{"empty", "/Users/me", "", ""},
		{"absolute", "/Users/me", "/opt/x", "/opt/x"},
		{"variable", "/Users/me", "$HOME/x", "$HOME/x"},
		{"unicode home", "/Users/zażółć", "~/gęślą", "/Users/zażółć/gęślą"},
		{"home with spaces", "/Users/a b", "~/my tool/x y", "/Users/a b/my tool/x y"},
		{"home with trailing slash", "/Users/me/", "~/x", "/Users/me/x"},
		{"root home", "/", "~/x", "/x"},
		{"root home bare", "/", "~", "/"},
		{"trailing slash bare", "/Users/me/", "~", "/Users/me"},
		{"many trailing slashes bare", "/Users/me//", "~", "/Users/me"},
		{"trailing slash empty rest", "/Users/me/", "~/", "/Users/me/"},
		{"no home", "", "~/x", "~/x"},
		{"no home bare", "", "~", "~"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpandHome(tt.home, tt.in); got != tt.want {
				t.Errorf("ExpandHome(%q, %q) = %q, want %q", tt.home, tt.in, got, tt.want)
			}
		})
	}
}

func TestMCPServerExpandHome(t *testing.T) {
	orig := MCPServer{
		Command:   "~/.local/bin/x",
		Args:      []string{"~", "~/cfg", "--dir=~/cfg", "~user/x", "plain"},
		Env:       map[string]string{"A": "~/a", "B": "~", "C": "x~/y", "D": "$HOME/d"},
		EnvVars:   []string{"~/NAME"},
		Headers:   map[string]string{"X": "~/h"},
		HeaderEnv: map[string]string{"Y": "~/y"},
	}
	snapshot := MCPServer{
		Command: orig.Command, Args: slices.Clone(orig.Args), Env: maps.Clone(orig.Env),
	}
	got := orig.ExpandHome("/Users/me")
	want := MCPServer{
		Command:   "/Users/me/.local/bin/x",
		Args:      []string{"/Users/me", "/Users/me/cfg", "--dir=~/cfg", "~user/x", "plain"},
		Env:       map[string]string{"A": "/Users/me/a", "B": "~", "C": "x~/y", "D": "$HOME/d"},
		EnvVars:   []string{"~/NAME"},
		Headers:   map[string]string{"X": "~/h"},
		HeaderEnv: map[string]string{"Y": "~/y"},
	}
	if got.Command != want.Command || !slices.Equal(got.Args, want.Args) || !maps.Equal(got.Env, want.Env) ||
		!slices.Equal(got.EnvVars, want.EnvVars) || !maps.Equal(got.Headers, want.Headers) || !maps.Equal(got.HeaderEnv, want.HeaderEnv) {
		t.Errorf("ExpandHome = %+v, want %+v", got, want)
	}
	if orig.Command != snapshot.Command || !slices.Equal(orig.Args, snapshot.Args) || !maps.Equal(orig.Env, snapshot.Env) {
		t.Errorf("ExpandHome changed its receiver: %+v", orig)
	}
}

func TestMCPServerExpandHomeKeepsNil(t *testing.T) {
	got := MCPServer{Command: "x"}.ExpandHome("/Users/me")
	if got.Args != nil || got.Env != nil {
		t.Errorf("nil args and env became %#v and %#v", got.Args, got.Env)
	}
	url := MCPServer{URL: "https://example.com/~/mcp"}.ExpandHome("/Users/me")
	if url.URL != "https://example.com/~/mcp" {
		t.Errorf("url = %q", url.URL)
	}
}

func TestMCPServerHasHomeRef(t *testing.T) {
	for _, tt := range []struct {
		name string
		s    MCPServer
		want bool
	}{
		{"none", MCPServer{Command: "npx", Args: []string{"-y"}}, false},
		{"command", MCPServer{Command: "~/bin/x"}, true},
		{"arg", MCPServer{Command: "x", Args: []string{"~/a"}}, true},
		{"env", MCPServer{Command: "x", Env: map[string]string{"A": "~/a"}}, true},
		{"middle", MCPServer{Command: "x", Args: []string{"--a=~/a"}}, false},
		{"other user", MCPServer{Command: "~u/x"}, false},
	} {
		if got := tt.s.HasHomeRef(); got != tt.want {
			t.Errorf("%s: HasHomeRef = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMCPServerHomeError(t *testing.T) {
	withRef := MCPServer{Command: "~/bin/x"}
	for _, tt := range []struct {
		name string
		s    MCPServer
		home string
		bad  bool
	}{
		{"absolute", withRef, testutil.Abs("/Users/me"), false},
		{"relative", withRef, "rel2", true},
		{"dot", withRef, ".", true},
		{"empty", withRef, "", true},
		{"relative without a tilde", MCPServer{Command: "npx", Args: []string{"-y", "--a=~/x"}}, "rel2", false},
		{"empty without a tilde", MCPServer{URL: "https://example.com/~/mcp"}, "", false},
		{"relative arg", MCPServer{Command: "x", Args: []string{"~"}}, "rel2", true},
		{"relative env", MCPServer{Command: "x", Env: map[string]string{"A": "~/a"}}, "rel2", true},
	} {
		err := tt.s.HomeError(tt.home)
		if (err != nil) != tt.bad {
			t.Errorf("%s: HomeError(%q) = %v", tt.name, tt.home, err)
		}
		if err != nil && !strings.Contains(err.Error(), "HOME must be an absolute path") {
			t.Errorf("%s: error = %v", tt.name, err)
		}
	}
}
