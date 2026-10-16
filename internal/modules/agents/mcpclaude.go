package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
)

// claudeMCP edits the user-scope mcpServers of Claude Code's .claude.json
// directly: `claude mcp get` starts the server it reports on, and a plan
// must not.
type claudeMCP struct{ m *Module }

func (claudeMCP) provider() string { return config.ProviderClaude }

func (c claudeMCP) path() string { return c.m.claudeJSONPath() }

func (claudeMCP) loginHint(string) string { return "sign in with /mcp in Claude Code" }

func (claudeMCP) removeHint(name string) string { return "claude mcp remove --scope user " + name }

func claudeServers(path string, o object) (object, error) {
	var servers object
	if raw, ok := o.get("mcpServers"); ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return servers, fmt.Errorf("%s mcpServers: %w", path, err)
		}
	}
	return servers, nil
}

func claudeEntry(v map[string]any) (mcpEntry, error) {
	if v["type"] == nil && v["command"] != nil {
		v["type"] = "stdio"
	}
	value, canon, err := normalizeMCP(v)
	if err != nil {
		return mcpEntry{}, err
	}
	return mcpEntry{canon: canon, text: prettyMCP(value), value: value}, nil
}

func (c claudeMCP) load(path string) (mcpFile, error) {
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
		if out[name], err = claudeEntry(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func claudeObject(s config.MCPServer) (object, error) {
	var o object
	set := o.set
	var errs []error
	if s.IsHTTP() {
		errs = append(errs, set("type", "http"), set("url", s.URL))
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
		if len(headers) > 0 {
			errs = append(errs, set("headers", headers))
		}
		if s.OAuth.HasClient() {
			oauth, err := claudeOAuth(s.OAuth)
			errs = append(errs, err, set("oauth", oauth))
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
	errs = append(errs, set("type", "stdio"), set("command", s.Command), set("args", args))
	if len(env) > 0 {
		errs = append(errs, set("env", env))
	}
	return o, errors.Join(errs...)
}

// claudeOAuth is the oauth object `claude mcp add --client-id --callback-port`
// stores; scopes is the key Claude reads for the scope, which the command
// has no flag for.
func claudeOAuth(o config.MCPOAuth) (object, error) {
	var out object
	errs := []error{out.set("clientId", o.ClientID)}
	if o.CallbackPort != 0 {
		errs = append(errs, out.set("callbackPort", o.CallbackPort))
	}
	if o.Scope != "" {
		errs = append(errs, out.set("scopes", o.Scope))
	}
	return out, errors.Join(errs...)
}

func (c claudeMCP) render(_ string, s config.MCPServer) (mcpEntry, error) {
	o, err := claudeObject(s)
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
	return claudeEntry(v)
}

func (c claudeMCP) set(path, name string, s config.MCPServer) error {
	entry, err := claudeObject(s)
	if err != nil {
		return err
	}
	return c.edit(path, func(servers *object) error { return servers.set(name, entry) })
}

func (c claudeMCP) unset(path, name string) error {
	return c.edit(path, func(servers *object) error {
		servers.delete(name)
		return nil
	})
}

func (c claudeMCP) edit(path string, change func(servers *object) error) error {
	return editMCPServers(c.m, path, change)
}

// editMCPServers changes the mcpServers object of the JSON config at path,
// the layout Claude Code's .claude.json and Copilot's mcp-config.json
// share, and leaves its other keys as they are.
func editMCPServers(m *Module, path string, change func(servers *object) error) error {
	return retryEdit(path, func() error {
		if err := install.CheckParents(m.Paths, path); err != nil {
			return err
		}
		if err := checkMCPFile(path); err != nil {
			return fmt.Errorf("%s: %w", m.Paths.Pretty(path), err)
		}
		data, err := readIfExists(path)
		if err != nil {
			return err
		}
		var o object
		if data != nil {
			if o, err = parseObject(path, data); err != nil {
				return err
			}
		}
		servers, err := claudeServers(path, o)
		if err != nil {
			return err
		}
		before, err := marshal(servers)
		if err != nil {
			return err
		}
		if err := change(&servers); err != nil {
			return err
		}
		after, err := marshal(servers)
		if err != nil {
			return err
		}
		if string(before) == string(after) {
			return nil
		}
		if len(servers.keys) == 0 {
			o.delete("mcpServers")
		} else if err := o.set("mcpServers", servers); err != nil {
			return err
		}
		if data != nil {
			return writeObjectIf(path, o, data)
		}
		return createObject(path, o, 0o600)
	})
}
