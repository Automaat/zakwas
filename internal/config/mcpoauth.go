package config

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
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
	ClientID     string `yaml:"clientId" jsonschema:"required,minLength=1,pattern=^[\\p{L}\\p{N}\\p{P}\\p{S}]+$" jsonschema_description:"OAuth client ID registered for this server with its authorization server or gateway, as a string: quote a value YAML reads as another type, such as '12345', 'true', '2024-01-01' or 'null'. A client ID is public, so it is written to the agents' config as is; the client secret is not supported, only public (PKCE) clients are. Only letters, digits, punctuation and symbols are accepted: spaces, control and invisible characters, combining marks, and the blank letters U+115F, U+1160, U+3164, U+FFA0 and U+2800 are rejected."`
	CallbackPort int    `yaml:"callbackPort" jsonschema:"minimum=1,maximum=65535" jsonschema_description:"Fixed port of the local OAuth callback, for a client whose redirect URIs are registered with a port. Claude Code and opencode take it as callbackPort, Codex as oauth.callback_port, Copilot CLI as auth.redirectPort and Gemini CLI inside redirectUri. Each provider uses its own callback path, so register every redirect URI you sign in with. Default: the provider's own."`
	Scope        string `yaml:"scope" jsonschema:"minLength=1,pattern=^[!#-\\[\\]-~]+( [!#-\\[\\]-~]+)*$" jsonschema_description:"OAuth scopes to request, as a string of visible ASCII scope names separated by single spaces; double quotes and backslashes are rejected. Default: the scopes the server announces."`
}

// On reports a server that signs in with OAuth, whichever form declares it.
func (o MCPOAuth) On() bool { return o.Enabled || o.ClientID != "" }

// HasClient reports the mapping form, which names a pre-registered client.
func (o MCPOAuth) HasClient() bool { return o.ClientID != "" }

// Scopes is Scope split into its names.
func (o MCPOAuth) Scopes() []string { return strings.Fields(o.Scope) }

var scopeText = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+( [\x21\x23-\x5B\x5D-\x7E]+)*$`)

const maxPort = 65535

// BlankGlyphs are the letters and symbols that print as nothing: the Hangul
// fillers and the braille blank. The schema repeats them in a "not" pattern.
const BlankGlyphs = "\u115f\u1160\u2800\u3164\uffa0"

// VisibleRune reports a code point a client ID or scope may hold: a printable
// ASCII character, or a letter, number, punctuation mark or symbol that
// isn't blank. Spaces, control and format characters, combining marks,
// private use and unassigned code points are out.
func VisibleRune(r rune) bool {
	if r < utf8.RuneSelf {
		return r > ' ' && r < 0x7f
	}
	if strings.ContainsRune(BlankGlyphs, r) {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
}

func (o *MCPOAuth) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	if n == nil {
		return errors.New("oauth: alias nesting is too deep")
	}
	switch n.Kind {
	case yaml.ScalarNode:
		var on bool
		if err := n.Decode(&on); err != nil {
			return fmt.Errorf("line %d: oauth must be true, false or a mapping with clientId", n.Line)
		}
		*o = MCPOAuth{Enabled: on}
		return nil
	case yaml.MappingNode:
		return o.decodeMapping(n)
	}
	return fmt.Errorf("line %d: oauth must be true, false or a mapping with clientId", n.Line)
}

func (o *MCPOAuth) decodeMapping(n *yaml.Node) error {
	seen := map[string]bool{}
	out := MCPOAuth{Enabled: true}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], resolveAlias(n.Content[i+1])
		if k.ShortTag() == "!!merge" {
			return fmt.Errorf("line %d: oauth doesn't take a merge key (<<); write clientId, callbackPort and scope out", k.Line)
		}
		if !slices.Contains([]string{"clientId", "callbackPort", "scope"}, k.Value) {
			return fmt.Errorf("line %d: unknown field in oauth; allowed: clientId, callbackPort, scope", k.Line)
		}
		if seen[k.Value] {
			return fmt.Errorf("line %d: oauth.%s is set twice", k.Line, k.Value)
		}
		seen[k.Value] = true
		if v == nil || v.Kind == yaml.ScalarNode && v.ShortTag() == "!!null" {
			continue
		}
		switch k.Value {
		case "clientId", "scope":
			if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
				return fmt.Errorf("line %d: oauth.%s must be a string; quote a value that YAML reads as another type: true, false, a number, a date or null", v.Line, k.Value)
			}
			if k.Value == "scope" {
				if v.Value == "" {
					return fmt.Errorf("line %d: oauth.scope is empty; remove it to request the server's default scopes", v.Line)
				}
				out.Scope = v.Value
			} else {
				out.ClientID = v.Value
			}
		case "callbackPort":
			port, ok := yamlPort(v)
			if !ok {
				return fmt.Errorf("line %d: oauth.callbackPort must be a whole number between 1 and %d", v.Line, maxPort)
			}
			out.CallbackPort = port
		}
	}
	if out.ClientID == "" {
		return fmt.Errorf("line %d: oauth.clientId is required in the mapping form; use oauth: true when the server registers its own client", n.Line)
	}
	*o = out
	return nil
}

// yamlPort reads a port from an integer node, or a float node that holds a
// whole number as the schema's integer type does (8080.0, 1e3).
func yamlPort(v *yaml.Node) (int, bool) {
	if v.Kind != yaml.ScalarNode {
		return 0, false
	}
	var f float64
	switch v.ShortTag() {
	case "!!int":
		var n int
		if v.Decode(&n) != nil {
			return 0, false
		}
		f = float64(n)
	case "!!float":
		if v.Decode(&f) != nil || f != math.Trunc(f) {
			return 0, false
		}
	default:
		return 0, false
	}
	if f < 1 || f > maxPort {
		return 0, false
	}
	return int(f), true
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
	if !utf8.ValidString(o.ClientID) || slices.ContainsFunc([]rune(o.ClientID), func(r rune) bool { return !VisibleRune(r) }) {
		add("oauth.clientId may only hold visible letters, digits, punctuation and symbols: no spaces, control, invisible or combining characters")
	}
	if o.CallbackPort != 0 && (o.CallbackPort < 1 || o.CallbackPort > maxPort) {
		add("oauth.callbackPort %d must be between 1 and %d", o.CallbackPort, maxPort)
	}
	if o.Scope != "" && (!scopeText.MatchString(o.Scope) || slices.ContainsFunc([]rune(o.Scope), func(r rune) bool { return r != ' ' && !VisibleRune(r) })) {
		add("oauth.scope must be visible ASCII scope names separated by single spaces, without double quotes or backslashes")
	}
	return out
}
