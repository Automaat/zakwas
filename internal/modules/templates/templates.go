// Package templates renders repo templates into protected copies, for configs
// that need machine values such as the home path.
package templates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
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
// agentPermissions function.
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
// one with " *" for its arguments. With the guard on, the commands that
// apply the config are denied last, also after a flag: zakwas * apply.
func agentPermissions(agents *config.Agents, provider string, catchAll ...string) (string, error) {
	if provider != config.ProviderOpencode {
		return "", fmt.Errorf("agentPermissions: provider %q is not supported, only %s", provider, config.ProviderOpencode)
	}
	if len(catchAll) > 1 {
		return "", fmt.Errorf("agentPermissions: want at most one default action, got %d", len(catchAll))
	}
	type rule struct{ pattern, action string }
	var rules []rule
	add := func(pattern, action string) {
		rules = slices.DeleteFunc(rules, func(r rule) bool { return r.pattern == pattern })
		rules = append(rules, rule{pattern, action})
	}
	if len(catchAll) == 1 {
		if !slices.Contains([]string{"allow", "ask", "deny"}, catchAll[0]) {
			return "", fmt.Errorf("agentPermissions: default action %q is not one of allow, ask, deny", catchAll[0])
		}
		add("*", catchAll[0])
	}
	for _, p := range agents.PermissionsFor(provider) {
		action := opencodeActions[p.Decision]
		add(p.Command(), action)
		add(p.Command()+" *", action)
	}
	for _, p := range agents.GuardPermissions(provider) {
		command := p.Command()
		first, rest := p.Words[0], strings.Join(p.Words[1:], " ")
		for _, pattern := range []string{command, command + " *", first + " * " + rest, first + " * " + rest + " *"} {
			add(pattern, opencodeActions[p.Decision])
		}
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, r := range rules {
		key, err := json.Marshal(r.pattern)
		if err != nil {
			return "", err
		}
		value, err := json.Marshal(r.action)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.Write(key)
		b.WriteString(": ")
		b.Write(value)
	}
	b.WriteByte('}')
	return b.String(), nil
}
