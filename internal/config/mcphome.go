package config

import (
	"maps"
	"slices"
	"strings"
)

// ExpandHome replaces a leading "~" in v with home: "~" alone, or "~"
// followed by "/", whose rest is kept as written. "~user/x", "~x" and a
// tilde anywhere else stay. Without a home, v is returned as is.
func ExpandHome(home, v string) string {
	if home == "" {
		return v
	}
	if v == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(v, "~/"); ok {
		return strings.TrimRight(home, "/") + "/" + rest
	}
	return v
}

// ExpandHome returns the server with "~" expanded to home in its command,
// in each of its args, and in the value of each env entry that starts with
// "~/". The url, headers, variable names and the OAuth client are not
// touched, and neither is $HOME. The receiver is not modified.
func (s MCPServer) ExpandHome(home string) MCPServer {
	s.Command = ExpandHome(home, s.Command)
	s.Args = slices.Clone(s.Args)
	for i, arg := range s.Args {
		s.Args[i] = ExpandHome(home, arg)
	}
	s.Env = maps.Clone(s.Env)
	for k, v := range s.Env {
		if strings.HasPrefix(v, "~/") {
			s.Env[k] = ExpandHome(home, v)
		}
	}
	return s
}

// HasHomeRef reports whether the server holds a value ExpandHome would
// change.
func (s MCPServer) HasHomeRef() bool {
	return !mcpEqualValues(s, s.ExpandHome("/"))
}

func mcpEqualValues(a, b MCPServer) bool {
	return a.Command == b.Command && slices.Equal(a.Args, b.Args) && maps.Equal(a.Env, b.Env)
}
