package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// MCPOAuth is agents.mcp.<name>.oauth. In YAML it is either true, a login
// hint for a server that registers its own client, or a mapping naming the
// pre-registered client every provider presents. No client secret has a
// place here: a secret never reaches the config repo.
type MCPOAuth struct {
	Enabled      bool   `yaml:"-"`
	ClientID     string `yaml:"clientId" jsonschema:"required,minLength=1,pattern=^\\S+$" jsonschema_description:"OAuth client ID registered for this server with its authorization server or gateway. A client ID is public, so it is written to the agents' config as is; the client secret is not supported, only public (PKCE) clients are."`
	CallbackPort int    `yaml:"callbackPort" jsonschema:"minimum=1,maximum=65535" jsonschema_description:"Fixed port of the local OAuth callback, for a client whose redirect URIs are registered with a port. Claude Code and opencode take it as callbackPort, Codex as oauth.callback_port, Copilot CLI as auth.redirectPort and Gemini CLI inside redirectUri. Each provider uses its own callback path, so register every redirect URI you sign in with. Default: the provider's own."`
	Scope        string `yaml:"scope" jsonschema:"minLength=1,pattern=^[!#-\\[\\]-~]+( [!#-\\[\\]-~]+)*$" jsonschema_description:"OAuth scopes to request, separated by single spaces. Default: the scopes the server announces."`
}

// On reports a server that signs in with OAuth, whichever form declares it.
func (o MCPOAuth) On() bool { return o.Enabled || o.ClientID != "" }

// HasClient reports the mapping form, which names a pre-registered client.
func (o MCPOAuth) HasClient() bool { return o.ClientID != "" }

// Scopes is Scope split into its names.
func (o MCPOAuth) Scopes() []string { return strings.Fields(o.Scope) }

var scopeText = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+( [\x21\x23-\x5B\x5D-\x7E]+)*$`)

const maxPort = 65535

func (o *MCPOAuth) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.ShortTag() != "!!bool" {
			return fmt.Errorf("line %d: oauth must be true, false or a mapping with clientId", n.Line)
		}
		var on bool
		if err := n.Decode(&on); err != nil {
			return err
		}
		*o = MCPOAuth{Enabled: on}
		return nil
	case yaml.MappingNode:
		if err := knownKeys(n, "clientId", "callbackPort", "scope"); err != nil {
			return err
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if v := n.Content[i+1]; n.Content[i].Value == "callbackPort" && v.Kind == yaml.ScalarNode && v.ShortTag() == "!!float" {
				return fmt.Errorf("line %d: oauth.callbackPort must be a whole number between 1 and %d", v.Line, maxPort)
			}
		}
		var raw struct {
			ClientID     *string `yaml:"clientId"`
			CallbackPort *int    `yaml:"callbackPort"`
			Scope        *string `yaml:"scope"`
		}
		if err := n.Decode(&raw); err != nil {
			return err
		}
		out := MCPOAuth{Enabled: true}
		if raw.ClientID != nil {
			out.ClientID = *raw.ClientID
		}
		if raw.CallbackPort != nil {
			out.CallbackPort = *raw.CallbackPort
			if out.CallbackPort < 1 || out.CallbackPort > maxPort {
				return fmt.Errorf("line %d: oauth.callbackPort %d must be between 1 and %d", n.Line, out.CallbackPort, maxPort)
			}
		}
		if raw.Scope != nil {
			out.Scope = *raw.Scope
			if out.Scope == "" {
				return fmt.Errorf("line %d: oauth.scope is empty; remove it to request the server's default scopes", n.Line)
			}
		}
		if out.ClientID == "" {
			return fmt.Errorf("line %d: oauth.clientId is required in the mapping form; use oauth: true when the server registers its own client", n.Line)
		}
		*o = out
		return nil
	}
	return fmt.Errorf("line %d: oauth must be true, false or a mapping with clientId", n.Line)
}

// Problems names what is wrong with the mapping form's fields, which a
// config built in Go skips the decoder for.
func (o MCPOAuth) Problems() []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	if !o.HasClient() {
		if o.CallbackPort != 0 || o.Scope != "" {
			add("oauth.clientId is required when oauth names a callbackPort or scope")
		}
		return out
	}
	if !utf8.ValidString(o.ClientID) || strings.ContainsFunc(o.ClientID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		add("oauth.clientId may not hold spaces or control characters")
	}
	if o.CallbackPort != 0 && (o.CallbackPort < 1 || o.CallbackPort > maxPort) {
		add("oauth.callbackPort %d must be between 1 and %d", o.CallbackPort, maxPort)
	}
	if o.Scope != "" && !scopeText.MatchString(o.Scope) {
		add("oauth.scope %q must be scope names separated by single spaces, without quotes or backslashes", o.Scope)
	}
	return out
}
