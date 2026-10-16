package config

import (
	"strings"
	"testing"
)

func TestMCPOAuthDecoding(t *testing.T) {
	const url = "url: 'https://mcp.example.com/slack'"
	for _, tt := range []struct {
		name   string
		oauth  string
		want   MCPOAuth
		reject string
	}{
		{"absent", "", MCPOAuth{}, ""},
		{"true", "oauth: true", MCPOAuth{Enabled: true}, ""},
		{"false", "oauth: false", MCPOAuth{}, ""},
		{"null", "oauth: null", MCPOAuth{}, ""},
		{"tilde", "oauth: ~", MCPOAuth{}, ""},
		{"client only", "oauth: {clientId: 0oa1abcdEXAMPLE}", MCPOAuth{Enabled: true, ClientID: "0oa1abcdEXAMPLE"}, ""},
		{"all fields", "oauth: {clientId: 0oa1abcdEXAMPLE, callbackPort: 54661, scope: 'read write'}", MCPOAuth{Enabled: true, ClientID: "0oa1abcdEXAMPLE", CallbackPort: 54661, Scope: "read write"}, ""},
		{"lowest port", "oauth: {clientId: c, callbackPort: 1}", MCPOAuth{Enabled: true, ClientID: "c", CallbackPort: 1}, ""},
		{"highest port", "oauth: {clientId: c, callbackPort: 65535}", MCPOAuth{Enabled: true, ClientID: "c", CallbackPort: 65535}, ""},
		{"numeric client id", "oauth: {clientId: '12345'}", MCPOAuth{Enabled: true, ClientID: "12345"}, ""},
		{"null port and scope", "oauth: {clientId: c, callbackPort: , scope: }", MCPOAuth{Enabled: true, ClientID: "c"}, ""},
		{"missing client id", "oauth: {callbackPort: 54661}", MCPOAuth{}, "oauth.clientId is required"},
		{"empty mapping", "oauth: {}", MCPOAuth{}, "oauth.clientId is required"},
		{"null client id", "oauth: {clientId: }", MCPOAuth{}, "oauth.clientId is required"},
		{"empty client id", "oauth: {clientId: ''}", MCPOAuth{}, "oauth.clientId is required"},
		{"client id with a space", "oauth: {clientId: 'a b'}", MCPOAuth{}, "agents.mcp.slack: oauth.clientId may not hold spaces or control characters"},
		{"port zero", "oauth: {clientId: c, callbackPort: 0}", MCPOAuth{}, "oauth.callbackPort 0 must be between 1 and 65535"},
		{"port negative", "oauth: {clientId: c, callbackPort: -1}", MCPOAuth{}, "oauth.callbackPort -1 must be between 1 and 65535"},
		{"port too high", "oauth: {clientId: c, callbackPort: 65536}", MCPOAuth{}, "oauth.callbackPort 65536 must be between 1 and 65535"},
		{"port as text", "oauth: {clientId: c, callbackPort: '54661'}", MCPOAuth{}, "cannot unmarshal"},
		{"port as float", "oauth: {clientId: c, callbackPort: 54661.5}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port as whole float", "oauth: {clientId: c, callbackPort: 54661.0}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port as list", "oauth: {clientId: c, callbackPort: [1]}", MCPOAuth{}, "cannot unmarshal"},
		{"empty scope", "oauth: {clientId: c, scope: ''}", MCPOAuth{}, "oauth.scope is empty"},
		{"scope with double spaces", "oauth: {clientId: c, scope: 'a  b'}", MCPOAuth{}, "oauth.scope"},
		{"scope with a quote", `oauth: {clientId: c, scope: 'a"b'}`, MCPOAuth{}, "oauth.scope"},
		{"scope with a backslash", `oauth: {clientId: c, scope: 'a\b'}`, MCPOAuth{}, "oauth.scope"},
		{"scope as list", "oauth: {clientId: c, scope: [a, b]}", MCPOAuth{}, "cannot unmarshal"},
		{"unknown key", "oauth: {clientId: c, clientSecret: s}", MCPOAuth{}, "field clientSecret not found, want one of clientId, callbackPort, scope"},
		{"snake case key", "oauth: {client_id: c}", MCPOAuth{}, "field client_id not found"},
		{"text", "oauth: yes please", MCPOAuth{}, "oauth must be true, false or a mapping with clientId"},
		{"number", "oauth: 1", MCPOAuth{}, "oauth must be true, false or a mapping with clientId"},
		{"list", "oauth: [c]", MCPOAuth{}, "oauth must be true, false or a mapping with clientId"},
		{"object with bearer token", "oauth: {clientId: c}, bearerTokenEnv: T", MCPOAuth{}, "oauth can't be combined with an Authorization header (bearerTokenEnv)"},
		{"object with authorization header", "oauth: {clientId: c}, headers: {authorization: x}", MCPOAuth{}, "oauth can't be combined with an Authorization header (headers)"},
		{"true with authorization header", "oauth: true, headers: {Authorization: x}", MCPOAuth{}, "oauth can't be combined with an Authorization header (headers)"},
		{"object with other headers", "oauth: {clientId: c}, headers: {X-Org: acme}", MCPOAuth{Enabled: true, ClientID: "c"}, ""},
		{"object with header env", "oauth: {clientId: c}, headerEnv: {X-Key: KEY}", MCPOAuth{Enabled: true, ClientID: "c"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields := url
			if tt.oauth != "" {
				fields += ", " + tt.oauth
			}
			c, err := Load(writeConfig(t, "agents: {mcp: {slack: {"+fields+"}}}"), "/h")
			if tt.reject != "" {
				if err == nil || !strings.Contains(err.Error(), tt.reject) {
					t.Fatalf("err = %v, want one mentioning %q", err, tt.reject)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Agents.MCP["slack"].OAuth; got != tt.want {
				t.Errorf("oauth = %+v, want %+v", got, tt.want)
			}
			if got := c.Agents.MCP["slack"].OAuth.On(); got != (tt.want.Enabled || tt.want.ClientID != "") {
				t.Errorf("On() = %v for %+v", got, tt.want)
			}
		})
	}
}

func TestMCPOAuthOnlyAppliesToHTTPServers(t *testing.T) {
	for name, oauth := range map[string]string{
		"true":   "true",
		"object": "{clientId: c}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "agents: {mcp: {a: {command: x, oauth: "+oauth+"}}}"), "/h")
			if err == nil || !strings.Contains(err.Error(), "agents.mcp.a: oauth only applies to a server with a url") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestMCPOAuthProblemsOfAConfigBuiltInGo(t *testing.T) {
	for _, tt := range []struct {
		name  string
		oauth MCPOAuth
		want  string
	}{
		{"port without a client", MCPOAuth{CallbackPort: 1}, "oauth.clientId is required"},
		{"scope without a client", MCPOAuth{Scope: "a"}, "oauth.clientId is required"},
		{"port out of range", MCPOAuth{ClientID: "c", CallbackPort: 70000}, "must be between 1 and 65535"},
		{"negative port", MCPOAuth{ClientID: "c", CallbackPort: -5}, "must be between 1 and 65535"},
		{"control character", MCPOAuth{ClientID: "c\x07"}, "may not hold spaces or control characters"},
		{"invalid UTF-8", MCPOAuth{ClientID: "c\xff"}, "may not hold spaces or control characters"},
		{"scope", MCPOAuth{ClientID: "c", Scope: " a"}, "oauth.scope"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.oauth.Problems()
			if len(got) != 1 || !strings.Contains(got[0], tt.want) {
				t.Errorf("problems = %q, want one mentioning %q", got, tt.want)
			}
		})
	}
	if got := (MCPOAuth{ClientID: "c", CallbackPort: 1, Scope: "a b"}).Problems(); len(got) != 0 {
		t.Errorf("valid oauth has problems: %q", got)
	}
}

func TestMCPOAuthScopes(t *testing.T) {
	if got := (MCPOAuth{Scope: "read write"}).Scopes(); len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Errorf("scopes = %q", got)
	}
	if got := (MCPOAuth{}).Scopes(); len(got) != 0 {
		t.Errorf("scopes = %q", got)
	}
}
