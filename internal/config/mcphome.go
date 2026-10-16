package config

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// homeTrim is what ExpandHome strips from the end of a home directory.
var homeTrim = func() string {
	if filepath.Separator == '/' {
		return "/"
	}
	return `/\`
}()

// ExpandHome replaces a leading "~" in v with home: "~" alone, or "~"
// followed by "/", whose rest is kept as written. "~user/x", "~x" and a
// tilde anywhere else stay. Without a home, v is returned as is.
func ExpandHome(home, v string) string {
	if home == "" {
		return v
	}
	base := strings.TrimRight(home, homeTrim)
	if v == "~" {
		return cmp.Or(base, "/")
	}
	if rest, ok := CutHome(v); ok {
		return base + "/" + rest
	}
	return v
}

// HomeError is the reason the server's ~ values can't be expanded against
// home, or nil: a relative home would put relative paths in the agents'
// config, so a server that needs the expansion fails instead.
func (s MCPServer) HomeError(home string) error {
	if filepath.IsAbs(home) || !s.HasHomeRef() {
		return nil
	}
	return fmt.Errorf("its ~ can't be expanded: HOME must be an absolute path, got %q", home)
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
		if _, ok := CutHome(v); ok {
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
