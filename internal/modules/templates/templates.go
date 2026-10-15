// Package templates renders repo templates into protected copies, for configs
// that need machine values such as the home path.
package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

const defaultMode fs.FileMode = 0o444

// Data is what templates can reference.
type Data struct {
	Home string
	Vars map[string]string
}

// Module renders templates. Agents, which may be nil, feeds the
// agentPermissions and agentMCP functions.
type Module struct {
	Templates config.Templates
	Agents    *config.Agents
	Paths     config.Paths
	Installer *install.Installer
}

func (m *Module) Name() string { return "templates" }

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	data := Data{Home: m.Paths.Home, Vars: m.Templates.Vars}
	var changes []engine.Change
	for _, f := range m.Templates.Files {
		mode := f.Mode
		if mode == 0 {
			mode = defaultMode
		}
		want, err := Render(m.Paths.Src(f.Src), data, Funcs(m.Agents))
		if err != nil {
			return nil, err
		}
		c, err := m.Installer.Plan(m.Paths.Dst(f.Dst), want, mode)
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return changes, nil
}

// Render executes the template at path, failing on unknown variables.
func Render(path string, data Data, funcs template.FuncMap) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := template.New(filepath.Base(path)).Option("missingkey=error").Funcs(funcs).Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Funcs are the functions templates can call; agents may be nil.
func Funcs(agents *config.Agents) template.FuncMap {
	return template.FuncMap{
		"agentPermissions": func(provider string, catchAll ...string) (string, error) {
			return agentPermissions(agents, provider, catchAll...)
		},
		"agentMCP": func(provider string) (string, error) {
			return agentMCP(agents, provider)
		},
	}
}

// opencodeActions maps a decision to opencode's permission action.
var opencodeActions = map[string]string{
	config.PermissionAllow:  "allow",
	config.PermissionReview: "ask",
	config.PermissionDeny:   "deny",
}

// agentPermissions renders agents.permissions as opencode's permission.bash
// object. opencode applies the last matching pattern, so the catch-all
// comes first and deny entries last; each prefix gets a bare pattern and
// one with " *" for its arguments.
func agentPermissions(agents *config.Agents, provider string, catchAll ...string) (string, error) {
	if provider != config.ProviderOpencode {
		return "", fmt.Errorf("agentPermissions: provider %q is not supported, only %s", provider, config.ProviderOpencode)
	}
	if len(catchAll) > 1 {
		return "", fmt.Errorf("agentPermissions: want at most one default action, got %d", len(catchAll))
	}
	var b strings.Builder
	add := func(pattern, action string) error {
		key, err := json.Marshal(pattern)
		if err != nil {
			return err
		}
		value, err := json.Marshal(action)
		if err != nil {
			return err
		}
		if b.Len() > 1 {
			b.WriteString(", ")
		}
		b.Write(key)
		b.WriteString(": ")
		b.Write(value)
		return nil
	}
	b.WriteByte('{')
	if len(catchAll) == 1 {
		if !slices.Contains([]string{"allow", "ask", "deny"}, catchAll[0]) {
			return "", fmt.Errorf("agentPermissions: default action %q is not one of allow, ask, deny", catchAll[0])
		}
		if err := add("*", catchAll[0]); err != nil {
			return "", err
		}
	}
	for _, p := range agents.PermissionsFor(provider) {
		action := opencodeActions[p.Decision]
		if err := errors.Join(add(p.Command(), action), add(p.Command()+" *", action)); err != nil {
			return "", err
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}

// opencodeMCP is one entry of opencode's mcp object.
type opencodeMCP struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	OAuth       *bool             `json:"oauth,omitempty"`
	Enabled     bool              `json:"enabled"`
}

func opencodeServer(s config.MCPServer) opencodeMCP {
	if !s.IsHTTP() {
		env := maps.Clone(s.Env)
		if env == nil {
			env = map[string]string{}
		}
		for _, name := range s.EnvVars {
			env[name] = "{env:" + name + "}"
		}
		return opencodeMCP{Type: "local", Command: append([]string{s.Command}, s.Args...), Environment: env, Enabled: true}
	}
	headers := maps.Clone(s.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	for h, name := range s.HeaderEnv {
		headers[h] = "{env:" + name + "}"
	}
	if s.BearerTokenEnv != "" {
		headers["Authorization"] = "Bearer {env:" + s.BearerTokenEnv + "}"
	}
	out := opencodeMCP{Type: "remote", URL: s.URL, Headers: headers, Enabled: true}
	if len(headers) > 0 {
		off := false
		out.OAuth = &off
	}
	return out
}

// agentMCP renders agents.mcp as opencode's mcp object, one line of JSON
// holding the servers that target opencode.
func agentMCP(agents *config.Agents, provider string) (string, error) {
	if provider != config.ProviderOpencode {
		return "", fmt.Errorf("agentMCP: provider %q is not supported, only %s", provider, config.ProviderOpencode)
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, s := range agents.MCPFor(provider) {
		if i > 0 {
			b.WriteString(", ")
		}
		key, err := json.Marshal(s.Name)
		if err != nil {
			return "", err
		}
		var value bytes.Buffer
		enc := json.NewEncoder(&value)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(opencodeServer(s.MCPServer)); err != nil {
			return "", err
		}
		b.Write(key)
		b.WriteString(": ")
		b.Write(bytes.TrimRight(value.Bytes(), "\n"))
	}
	b.WriteByte('}')
	return b.String(), nil
}
