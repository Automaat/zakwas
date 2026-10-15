package agents

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
)

// codexMCP edits the [mcp_servers.*] tables of Codex's config.toml as text:
// `codex mcp add` rewrites every table of the file, dropping its comments,
// and opens a browser for OAuth servers.
type codexMCP struct{ m *Module }

func (codexMCP) provider() string { return config.ProviderCodex }

func (c codexMCP) path() string { return filepath.Join(c.m.codexBackend().home, "config.toml") }

func (codexMCP) loginHint(name string) string { return "sign in with `codex mcp login " + name + "`" }

func (codexMCP) removeHint(name string) string { return "codex mcp remove " + name }

func codexEntry(v map[string]any) (mcpEntry, error) {
	if vars, ok := v["env_vars"].([]any); ok {
		sorted := slices.Clone(vars)
		slices.SortFunc(sorted, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
		v = maps.Clone(v)
		v["env_vars"] = sorted
	}
	value, canon, err := normalizeMCP(v)
	if err != nil {
		return mcpEntry{}, err
	}
	return mcpEntry{canon: canon, value: value}, nil
}

func parseCodexServers(path string, data []byte) (map[string]mcpEntry, error) {
	var cfg struct {
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := map[string]mcpEntry{}
	for name, v := range cfg.Servers {
		entry, err := codexEntry(v)
		if err != nil {
			return nil, err
		}
		out[name] = entry
	}
	return out, nil
}

func (c codexMCP) load(path string) (mcpFile, error) {
	if err := checkMCPFile(path); err != nil {
		return nil, fmt.Errorf("%s: %w", c.m.Paths.Pretty(path), err)
	}
	data, err := readIfExists(path)
	if err != nil {
		return nil, err
	}
	servers, err := parseCodexServers(path, data)
	if err != nil {
		return nil, err
	}
	lines := splitLines(data)
	out := mcpFile{}
	for name, entry := range servers {
		regions := serverRegions(lines, name)
		var parts []string
		for _, r := range regions {
			parts = append(parts, strings.Join(lines[r[0]:r[1]], "\n"))
		}
		entry.text = strings.Join(parts, "\n\n") + "\n"
		entry.inline = len(regions) == 0
		out[name] = entry
	}
	return out, nil
}

func (c codexMCP) render(name string, s config.MCPServer) (mcpEntry, error) {
	text := renderCodexMCP(name, s)
	servers, err := parseCodexServers("zakwas.yaml", []byte(text))
	if err != nil {
		return mcpEntry{}, fmt.Errorf("renders as invalid TOML: %w", err)
	}
	entry := servers[name]
	entry.text = text
	return entry, nil
}

func (c codexMCP) set(path, name string, s config.MCPServer) error {
	want, err := c.render(name, s)
	if err != nil {
		return err
	}
	return c.edit(path, name, func(data []byte, inline bool) ([]byte, error) {
		if next, ok := replaceServer(data, name, want.text); ok {
			return next, nil
		}
		if inline {
			return nil, fmt.Errorf("%q is defined inline, which zakwas can't edit; change it by hand", name)
		}
		return appendServer(data, want.text), nil
	}, &want)
}

func (c codexMCP) unset(path, name string) error {
	return c.edit(path, name, func(data []byte, inline bool) ([]byte, error) {
		if next, ok := removeServer(data, name); ok {
			return next, nil
		}
		if inline {
			return nil, fmt.Errorf("%q is defined inline, which zakwas can't remove; delete it by hand", name)
		}
		return data, nil
	}, nil)
}

func (c codexMCP) edit(path, name string, change func(data []byte, inline bool) ([]byte, error), want *mcpEntry) error {
	return retryEdit(path, func() error {
		if err := install.CheckParents(c.m.Paths, path); err != nil {
			return err
		}
		if err := checkMCPFile(path); err != nil {
			return fmt.Errorf("%s: %w", c.m.Paths.Pretty(path), err)
		}
		data, err := readIfExists(path)
		if err != nil {
			return err
		}
		before, err := parseCodexServers(path, data)
		if err != nil {
			return err
		}
		_, inline := before[name]
		inline = inline && len(serverRegions(splitLines(data), name)) == 0
		next, err := change(data, inline)
		if err != nil {
			return err
		}
		if bytes.Equal(next, data) {
			return nil
		}
		if err := verifyCodexEdit(path, before, next, name, want); err != nil {
			return err
		}
		if data != nil {
			return writeBytesIf(path, next, data)
		}
		return createBytes(path, next, 0o600)
	})
}

func verifyCodexEdit(path string, before map[string]mcpEntry, next []byte, name string, want *mcpEntry) error {
	after, err := parseCodexServers(path, next)
	if err != nil {
		return fmt.Errorf("refusing to write an edit that breaks the file: %w", err)
	}
	for n, e := range before {
		if n != name && after[n].canon != e.canon {
			return fmt.Errorf("refusing to write an edit that changes server %q", n)
		}
	}
	for n := range after {
		if _, had := before[n]; !had && n != name {
			return fmt.Errorf("refusing to write an edit that adds server %q", n)
		}
	}
	switch got, ok := after[name]; {
	case want == nil && ok:
		return fmt.Errorf("refusing to write an edit that leaves server %q in place", name)
	case want != nil && got.canon != want.canon:
		return fmt.Errorf("refusing to write an edit that does not produce server %q as declared", name)
	}
	return nil
}

func checkMCPFile(path string) error {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return errors.New("not a regular file")
	}
	return nil
}

func retryEdit(path string, attempt func() error) error {
	for range claudeWriteAttempts {
		if err := attempt(); !errors.Is(err, errChanged) {
			return err
		}
	}
	return fmt.Errorf("%s kept changing while zakwas edited it; run apply again", path)
}
