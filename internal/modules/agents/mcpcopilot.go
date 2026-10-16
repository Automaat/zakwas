package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/config"
)

// copilotMCP edits the user-level mcpServers of Copilot CLI's
// mcp-config.json in place, the way `copilot mcp add` does, leaving every
// other server in it alone.
type copilotMCP struct{ m *Module }

func (copilotMCP) provider() string { return config.ProviderCopilot }

func (c copilotMCP) path() string { return filepath.Join(c.m.copilotBackend().home, "mcp-config.json") }

func (copilotMCP) loginHint(name string) string {
	return "sign in with `/mcp auth " + name + "` in Copilot CLI"
}

func (copilotMCP) removeHint(name string) string { return "copilot mcp remove " + name }

// copilotEntry normalizes the spellings Copilot reads alike: stdio is local,
// a missing type of a command is local, and a missing tools list means all.
func copilotEntry(v map[string]any) (mcpEntry, error) {
	if v == nil {
		return mcpEntry{}, errors.New("server entry is null")
	}
	if v["type"] == "stdio" || (v["type"] == nil && v["command"] != nil) {
		v["type"] = "local"
	}
	if v["tools"] == nil {
		v["tools"] = []any{"*"}
	}
	value, canon, err := normalizeMCP(v)
	if err != nil {
		return mcpEntry{}, err
	}
	return mcpEntry{canon: canon, text: prettyMCP(value), value: value}, nil
}

func (c copilotMCP) load(path string) (mcpFile, error) {
	if err := checkMCPFile(path); err != nil {
		return nil, fmt.Errorf("%s: %w", c.m.Paths.Pretty(path), err)
	}
	o, _, err := readObject(path)
	if err != nil {
		return nil, err
	}
	servers, err := claudeServers(path, o)
	if err != nil {
		return nil, err
	}
	out := mcpFile{}
	for _, name := range servers.keys {
		var v map[string]any
		if err := json.Unmarshal(servers.values[name], &v); err != nil {
			return nil, fmt.Errorf("%s mcpServers.%s: %w", path, name, err)
		}
		if v == nil {
			continue
		}
		if out[name], err = copilotEntry(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// copilotObject is the entry Copilot reads for s. It expands ${NAME} in env
// values and headers from its own environment, which is how the secrets
// named in zakwas.yaml reach the server without being written down.
func copilotObject(s config.MCPServer) (object, error) {
	var o object
	set := o.set
	tools := []string{"*"}
	if s.IsHTTP() {
		headers := maps.Clone(s.Headers)
		if headers == nil {
			headers = map[string]string{}
		}
		for h, env := range s.HeaderEnv {
			headers[h] = "${" + env + "}"
		}
		if s.BearerTokenEnv != "" {
			headers["Authorization"] = "Bearer ${" + s.BearerTokenEnv + "}"
		}
		errs := []error{set("tools", tools), set("type", "http"), set("url", s.URL)}
		if len(headers) > 0 {
			errs = append(errs, set("headers", headers))
		}
		if s.OAuth.HasClient() {
			errs = append(errs, set("oauthClientId", s.OAuth.ClientID), set("oauthPublicClient", true))
			if scopes := s.OAuth.Scopes(); len(scopes) > 0 {
				errs = append(errs, set("oauthScopes", scopes))
			}
			if s.OAuth.CallbackPort != 0 {
				errs = append(errs, set("auth", map[string]int{"redirectPort": s.OAuth.CallbackPort}))
			}
		}
		return o, errors.Join(errs...)
	}
	env := maps.Clone(s.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, name := range s.EnvVars {
		env[name] = "${" + name + "}"
	}
	args := s.Args
	if args == nil {
		args = []string{}
	}
	errs := []error{set("tools", tools), set("type", "local"), set("command", s.Command), set("args", args)}
	if len(env) > 0 {
		errs = append(errs, set("env", env))
	}
	return o, errors.Join(errs...)
}

func (c copilotMCP) render(_ string, s config.MCPServer) (mcpEntry, error) {
	o, err := copilotObject(s)
	if err != nil {
		return mcpEntry{}, err
	}
	data, err := marshal(o)
	if err != nil {
		return mcpEntry{}, err
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return mcpEntry{}, err
	}
	return copilotEntry(v)
}

func (c copilotMCP) set(path, name string, s config.MCPServer) error {
	entry, err := copilotObject(s)
	if err != nil {
		return err
	}
	return editMCPServers(c.m, path, func(servers *object) error { return servers.set(name, entry) })
}

func (c copilotMCP) unset(path, name string) error {
	return editMCPServers(c.m, path, func(servers *object) error {
		servers.delete(name)
		return nil
	})
}
