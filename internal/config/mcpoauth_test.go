package config

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
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
		{"client id with a space", "oauth: {clientId: 'a b'}", MCPOAuth{}, "agents.mcp.slack: oauth.clientId may only hold visible"},
		{"port zero", "oauth: {clientId: c, callbackPort: 0}", MCPOAuth{}, "oauth.callbackPort must be a whole number between 1 and 65535"},
		{"port negative", "oauth: {clientId: c, callbackPort: -1}", MCPOAuth{}, "oauth.callbackPort must be a whole number between 1 and 65535"},
		{"port too high", "oauth: {clientId: c, callbackPort: 65536}", MCPOAuth{}, "oauth.callbackPort must be a whole number between 1 and 65535"},
		{"port as text", "oauth: {clientId: c, callbackPort: '54661'}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port as float", "oauth: {clientId: c, callbackPort: 54661.5}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port as whole float", "oauth: {clientId: c, callbackPort: 54661.0}", MCPOAuth{Enabled: true, ClientID: "c", CallbackPort: 54661}, ""},
		{"port as exponent", "oauth: {clientId: c, callbackPort: 1e3}", MCPOAuth{Enabled: true, ClientID: "c", CallbackPort: 1000}, ""},
		{"port as hex", "oauth: {clientId: c, callbackPort: 0x1F90}", MCPOAuth{Enabled: true, ClientID: "c", CallbackPort: 8080}, ""},
		{"port as infinity", "oauth: {clientId: c, callbackPort: .inf}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port beyond int", "oauth: {clientId: c, callbackPort: 99999999999999999999}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"port as list", "oauth: {clientId: c, callbackPort: [1]}", MCPOAuth{}, "oauth.callbackPort must be a whole number"},
		{"empty scope", "oauth: {clientId: c, scope: ''}", MCPOAuth{}, "oauth.scope is empty"},
		{"scope with double spaces", "oauth: {clientId: c, scope: 'a  b'}", MCPOAuth{}, "oauth.scope"},
		{"scope with a quote", `oauth: {clientId: c, scope: 'a"b'}`, MCPOAuth{}, "oauth.scope"},
		{"scope with a backslash", `oauth: {clientId: c, scope: 'a\b'}`, MCPOAuth{}, "oauth.scope"},
		{"scope as list", "oauth: {clientId: c, scope: [a, b]}", MCPOAuth{}, "oauth.scope must be a string"},
		{"unknown key", "oauth: {clientId: c, clientSecret: s}", MCPOAuth{}, "line 1: unknown field in oauth; allowed: clientId, callbackPort, scope"},
		{"snake case key", "oauth: {client_id: c}", MCPOAuth{}, "unknown field in oauth; allowed: clientId, callbackPort, scope"},
		{"client id as number", "oauth: {clientId: 12}", MCPOAuth{}, "oauth.clientId must be a string; quote a value that YAML reads as another type: true, false, a number, a date or null"},
		{"client id as boolean", "oauth: {clientId: true}", MCPOAuth{}, "oauth.clientId must be a string; quote a value that YAML reads as another type: true, false, a number, a date or null"},
		{"client id as date", "oauth: {clientId: 2001-01-01}", MCPOAuth{}, "oauth.clientId must be a string"},
		{"scope as number", "oauth: {clientId: c, scope: 12}", MCPOAuth{}, "oauth.scope must be a string; quote a value that YAML reads as another type"},
		{"client id as yes is a string", "oauth: {clientId: yes}", MCPOAuth{Enabled: true, ClientID: "yes"}, ""},
		{"client id as a quoted merge key", `oauth: {clientId: "<<"}`, MCPOAuth{Enabled: true, ClientID: "<<"}, ""},
		{"client id as an unquoted merge key", "oauth: {clientId: <<}", MCPOAuth{}, "oauth.clientId must be a string"},
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
		{"control character", MCPOAuth{ClientID: "c\x07"}, "may only hold visible"},
		{"invisible character", MCPOAuth{ClientID: "c\u202ec"}, "may only hold visible"},
		{"zero-width space", MCPOAuth{ClientID: "c\u200bc"}, "may only hold visible"},
		{"invalid UTF-8", MCPOAuth{ClientID: "c\xff"}, "may only hold visible"},
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

func TestMCPOAuthPortThroughAnAlias(t *testing.T) {
	for _, tt := range []struct {
		name, port string
		reject     bool
	}{
		{"whole number", "8080", false},
		{"fraction", "8080.9", true},
		{"whole float", "8080.0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := "agents: {mcp: {t: {command: x, args: [&p " + tt.port + "]}, s: {url: 'https://e.com', oauth: {clientId: c, callbackPort: *p}}}}"
			_, err := Load(writeConfig(t, body), "/h")
			if tt.reject != (err != nil) {
				t.Fatalf("err = %v, reject %v", err, tt.reject)
			}
			if err != nil && !strings.Contains(err.Error(), "oauth.callbackPort must be a whole number") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestMCPOAuthProblemsNeverEchoAValue(t *testing.T) {
	for _, o := range []MCPOAuth{
		{ClientID: "c", Scope: `read "LEAKSCOPE1" \x`},
		{ClientID: "LEAKID1 x"},
		{ClientID: "LEAKID2\x07"},
		{ClientID: "LEAKID3", CallbackPort: 70000, Scope: "LEAKSCOPE2  x"},
	} {
		for _, p := range o.Problems() {
			if strings.Contains(p, "LEAK") {
				t.Errorf("problem %q echoes a value of %+v", p, o)
			}
		}
	}
}

func TestMCPOAuthBooleanSpellings(t *testing.T) {
	for want, spellings := range map[bool][]string{
		true:  {"true", "True", "TRUE", "yes", "Yes", "YES", "on", "On", "ON", "y", "Y", "'yes'"},
		false: {"false", "False", "FALSE", "no", "No", "NO", "off", "Off", "OFF", "n", "N", "'off'"},
	} {
		for _, spelling := range spellings {
			t.Run(spelling, func(t *testing.T) {
				c, err := Load(writeConfig(t, "agents: {mcp: {s: {url: 'https://e.com/x', oauth: "+spelling+"}}}"), "/h")
				if err != nil {
					t.Fatal(err)
				}
				if got := c.Agents.MCP["s"].OAuth; got != (MCPOAuth{Enabled: want}) {
					t.Errorf("oauth = %+v, want enabled %v", got, want)
				}
			})
		}
	}
	for i, spelling := range []string{"maybe", "tru", "2", "'yes please'", "~~"} {
		t.Run(fmt.Sprintf("rejects %d", i), func(t *testing.T) {
			_, err := Load(writeConfig(t, "agents: {mcp: {s: {url: 'https://e.com/x', oauth: "+spelling+"}}}"), "/h")
			if err == nil || !strings.Contains(err.Error(), "oauth must be true, false or a mapping with clientId") {
				t.Errorf("err = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "maybe") {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestMCPOAuthThroughAliases(t *testing.T) {
	body := `
agents:
  mcp:
    a: {url: 'https://e.com/a', oauth: &on yes}
    b: {url: 'https://e.com/b', oauth: *on}
    c: {url: 'https://e.com/c', oauth: &client {clientId: id, callbackPort: &port 8080, scope: &scope 'a b'}}
    d: {url: 'https://e.com/d', oauth: *client}
    e: {url: 'https://e.com/e', oauth: {clientId: *cid, callbackPort: *port, scope: *scope}}
    f: {url: 'https://e.com/f', args: [&cid other]}
`
	body = strings.Replace(body, "    f: {url: 'https://e.com/f', args: [&cid other]}\n", "", 1)
	body = strings.Replace(body, "clientId: *cid", "clientId: *id", 1)
	body = strings.Replace(body, "oauth: &client {clientId: id,", "oauth: &client {clientId: &id idvalue,", 1)
	c, err := Load(writeConfig(t, body), "/h")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]MCPOAuth{
		"a": {Enabled: true},
		"b": {Enabled: true},
		"c": {Enabled: true, ClientID: "idvalue", CallbackPort: 8080, Scope: "a b"},
		"d": {Enabled: true, ClientID: "idvalue", CallbackPort: 8080, Scope: "a b"},
		"e": {Enabled: true, ClientID: "idvalue", CallbackPort: 8080, Scope: "a b"},
	} {
		if got := c.Agents.MCP[name].OAuth; got != want {
			t.Errorf("%s: oauth = %+v, want %+v", name, got, want)
		}
	}
}

func TestMCPOAuthDoesNotEchoValuesInDecodeErrors(t *testing.T) {
	for _, tt := range []struct{ name, oauth, want string }{
		{"port text", "{clientId: c, callbackPort: SECRETMATERIAL}", "oauth.callbackPort must be a whole number"},
		{"port quoted", "{clientId: c, callbackPort: 'SECRETMATERIAL'}", "oauth.callbackPort must be a whole number"},
		{"port list", "{clientId: c, callbackPort: [SECRETMATERIAL]}", "oauth.callbackPort must be a whole number"},
		{"port map", "{clientId: c, callbackPort: {k: SECRETMATERIAL}}", "oauth.callbackPort must be a whole number"},
		{"client list", "{clientId: [SECRETMATERIAL]}", "oauth.clientId must be a string"},
		{"client map", "{clientId: {k: SECRETMATERIAL}}", "oauth.clientId must be a string"},
		{"client number", "{clientId: 123456789SECRET}", ""},
		{"scope list", "{clientId: c, scope: [SECRETMATERIAL]}", "oauth.scope must be a string"},
		{"scope bool", "{clientId: c, scope: true}", "oauth.scope must be a string"},
		{"text", "SECRETMATERIAL", "oauth must be true, false or a mapping with clientId"},
		{"list", "[SECRETMATERIAL]", "oauth must be true, false or a mapping with clientId"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "agents: {mcp: {s: {url: 'https://e.com/x', oauth: "+tt.oauth+"}}}"), "/h")
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error echoes the value: %v", err)
			}
		})
	}
}

func TestMCPOAuthStringScalars(t *testing.T) {
	for _, tt := range []struct {
		oauth  string
		client string
		reject bool
	}{
		{"{clientId: 12345}", "", true},
		{"{clientId: '12345'}", "12345", false},
		{`{clientId: "12345"}`, "12345", false},
		{"{clientId: true}", "", true},
		{"{clientId: 'true'}", "true", false},
		{"{clientId: 1.5}", "", true},
		{"{clientId: 0x10}", "", true},
		{"{clientId: yes}", "yes", false},
		{"{clientId: 0oa1abcdEXAMPLE}", "0oa1abcdEXAMPLE", false},
		{"{clientId: c, scope: 5}", "", true},
		{"{clientId: c, scope: true}", "", true},
		{"{clientId: c, scope: '5'}", "c", false},
	} {
		t.Run(tt.oauth, func(t *testing.T) {
			c, err := Load(writeConfig(t, "agents: {mcp: {s: {url: 'https://e.com/x', oauth: "+tt.oauth+"}}}"), "/h")
			if tt.reject {
				if err == nil || !strings.Contains(err.Error(), "must be a string") {
					t.Errorf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Agents.MCP["s"].OAuth.ClientID; got != tt.client {
				t.Errorf("clientId = %q", got)
			}
		})
	}
}

func TestMCPOAuthMergeAndDuplicateKeys(t *testing.T) {
	for _, tt := range []struct{ name, oauth, want string }{
		{"merge key", "{<<: {clientId: c}, callbackPort: 1}", "oauth doesn't take a merge key"},
		{"duplicate key", "{clientId: a, clientId: b}", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "agents: {mcp: {s: {url: 'https://e.com/x', oauth: "+tt.oauth+"}}}"), "/h")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVisibleRune(t *testing.T) {
	invisible := []rune{
		0x3164, 0x115F, 0x1160, 0xFFA0, 0x2800, 0x034F, 0xFE0F, 0xE0100, 0x17B4, 0x17B5,
		0x200B, 0x200C, 0x200D, 0x200E, 0x202E, 0x00AD, 0x2062, 0x180E, 0x00A0, 0x2003, 0x3000, 0x0085, 0x2028, 0x2029,
		0xFEFF, 0x061C, 0x2060, 0xE000, 0xE0001, 0xFFFE, 0x0378, 0x0301, 0x20DD, ' ', '\t', '\n', 0x7F, 0x00,
	}
	for _, r := range invisible {
		if VisibleRune(r) {
			t.Errorf("U+%04X is accepted", r)
		}
		if ids := (MCPOAuth{ClientID: "a" + string(r) + "b"}).Problems(); len(ids) == 0 {
			t.Errorf("a client ID with U+%04X has no problems", r)
		}
	}
	for _, r := range []rune{'a', 'Z', '0', '-', '_', '.', ':', '/', '$', '~', '!', '\'', '"', '\\', 'é', 'ż', '中', '٣', '€', '—'} {
		if !VisibleRune(r) {
			t.Errorf("%q is rejected", r)
		}
	}
}

func TestVisibleRuneSweep(t *testing.T) {
	defaultIgnorable := []unicode.Range16{
		{Lo: 0x00AD, Hi: 0x00AD, Stride: 1},
		{Lo: 0x034F, Hi: 0x034F, Stride: 1},
		{Lo: 0x061C, Hi: 0x061C, Stride: 1},
		{Lo: 0x115F, Hi: 0x1160, Stride: 1},
		{Lo: 0x17B4, Hi: 0x17B5, Stride: 1},
		{Lo: 0x180B, Hi: 0x180F, Stride: 1},
		{Lo: 0x200B, Hi: 0x200F, Stride: 1},
		{Lo: 0x202A, Hi: 0x202E, Stride: 1},
		{Lo: 0x2060, Hi: 0x206F, Stride: 1},
		{Lo: 0x3164, Hi: 0x3164, Stride: 1},
		{Lo: 0xFE00, Hi: 0xFE0F, Stride: 1},
		{Lo: 0xFEFF, Hi: 0xFEFF, Stride: 1},
		{Lo: 0xFFA0, Hi: 0xFFA0, Stride: 1},
		{Lo: 0xFFF0, Hi: 0xFFF8, Stride: 1},
	}
	defaultIgnorableWide := []unicode.Range32{
		{Lo: 0x1BCA0, Hi: 0x1BCA3, Stride: 1}, {Lo: 0x1D173, Hi: 0x1D17A, Stride: 1}, {Lo: 0xE0000, Hi: 0xE0FFF, Stride: 1},
	}
	ignorable := &unicode.RangeTable{R16: defaultIgnorable, R32: defaultIgnorableWide}
	accepted := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		got := VisibleRune(r)
		if got {
			accepted++
		}
		if got && unicode.Is(ignorable, r) {
			t.Errorf("U+%04X is Default_Ignorable_Code_Point but accepted", r)
		}
		if got && r != 0x2800 && !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			t.Errorf("U+%04X is outside letters, numbers, punctuation and symbols but accepted", r)
		}
		if got && (unicode.IsSpace(r) || unicode.IsControl(r) || unicode.IsMark(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r)) {
			t.Errorf("U+%04X is a space, control, mark, format or private use character but accepted", r)
		}
		if !got && r > 0x20 && r < 0x7F {
			t.Errorf("printable ASCII %q is rejected", r)
		}
	}
	if accepted < 100000 {
		t.Errorf("only %d code points accepted, the sweep is not reaching the planes", accepted)
	}
}

func TestLoadErrorsDoNotEchoKeys(t *testing.T) {
	const secret = "ghp_SECRETKEY123"
	for _, tt := range []struct {
		name, body, want string
	}{
		{"oauth", "agents: {mcp: {a: {url: 'http://x', oauth: {clientId: c, " + secret + ": 1}}}}", "line 1: unknown field in oauth; allowed: clientId, callbackPort, scope"},
		{"oauth on its own line", "agents:\n  mcp:\n    a:\n      url: http://x\n      oauth:\n        clientId: c\n        " + secret + ": 1\n", "line 7: unknown field in oauth"},
		{"top level", secret + ": 1", "line 1: unknown field in type config.Config"},
		{"nested type", "agents: {mcp: {a: {command: x, " + secret + ": y}}}", "line 1: unknown field in type config.MCPServer"},
		{"marketplace", "agents: {marketplaces: {a: {source: x, " + secret + ": y}}}", "line 1: unknown field in a marketplace; allowed: source, providers"},
		{"plugin", "agents: {plugins: [{id: a@b, " + secret + ": y}]}", "line 1: unknown field in a plugin; allowed: id, providers"},
		{"skill", "agents: {skills: [{path: a, " + secret + ": y}]}", "line 1: unknown field in a skill"},
		{"permission", "agents: {permissions: {allow: [{command: ls, " + secret + ": y}]}}", "line 1: unknown field in a permission"},
		{"unknown anchor", "agents: {mcp: {a: *" + secret + "}}", "unknown anchor referenced"},
		{"anchor that contains itself", "agents: {mcp: {a: &" + secret + " {command: x, env: *" + secret + "}}}", "an anchor's value contains itself"},
		{"duplicate key", "agents:\n  providers: [claude]\n  providers: [codex]\n", "line 3: duplicate mapping key; the first one is at line 2"},
		{"duplicate secret key", "agents:\n  mcp:\n    " + secret + ": {command: x}\n    " + secret + ": {command: y}\n", "line 4: duplicate mapping key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body), "/h")
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "SECRETKEY") {
				t.Errorf("error repeats the key: %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}
