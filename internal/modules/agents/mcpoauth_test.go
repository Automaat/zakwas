package agents

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/testutil"
)

const oauthClientID = "0oa1abcdEXAMPLE"

func oauthServer(o config.MCPOAuth) config.MCPServer {
	return config.MCPServer{URL: "https://mcp.example.com/slack", OAuth: o}
}

func TestMCPOAuthClientRendering(t *testing.T) {
	full := config.MCPOAuth{ClientID: oauthClientID, CallbackPort: 54661, Scope: "read write"}
	idOnly := config.MCPOAuth{ClientID: oauthClientID}
	for _, tt := range []struct {
		name     string
		provider string
		oauth    config.MCPOAuth
		file     func(m *Module) string
		want     string
	}{
		{
			"claude full", config.ProviderClaude, full,
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{
  "mcpServers": {
    "slack": {
      "type": "http",
      "url": "https://mcp.example.com/slack",
      "oauth": {
        "clientId": "0oa1abcdEXAMPLE",
        "callbackPort": 54661,
        "scopes": "read write"
      }
    }
  }
}
`,
		},
		{
			"claude client only", config.ProviderClaude, idOnly,
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{
  "mcpServers": {
    "slack": {
      "type": "http",
      "url": "https://mcp.example.com/slack",
      "oauth": {
        "clientId": "0oa1abcdEXAMPLE"
      }
    }
  }
}
`,
		},
		{
			"codex full", config.ProviderCodex, full,
			func(m *Module) string { return filepath.Join(m.CodexHome, "config.toml") },
			`[mcp_servers.slack]
url = "https://mcp.example.com/slack"
scopes = ["read", "write"]

[mcp_servers.slack.oauth]
client_id = "0oa1abcdEXAMPLE"
callback_port = 54661
`,
		},
		{
			"codex client only", config.ProviderCodex, idOnly,
			func(m *Module) string { return filepath.Join(m.CodexHome, "config.toml") },
			`[mcp_servers.slack]
url = "https://mcp.example.com/slack"

[mcp_servers.slack.oauth]
client_id = "0oa1abcdEXAMPLE"
`,
		},
		{
			"copilot full", config.ProviderCopilot, full,
			func(m *Module) string { return filepath.Join(m.CopilotHome, "mcp-config.json") },
			`{
  "mcpServers": {
    "slack": {
      "tools": [
        "*"
      ],
      "type": "http",
      "url": "https://mcp.example.com/slack",
      "oauthClientId": "0oa1abcdEXAMPLE",
      "oauthPublicClient": true,
      "oauthScopes": [
        "read",
        "write"
      ],
      "auth": {
        "redirectPort": 54661
      }
    }
  }
}
`,
		},
		{
			"copilot client only", config.ProviderCopilot, idOnly,
			func(m *Module) string { return filepath.Join(m.CopilotHome, "mcp-config.json") },
			`{
  "mcpServers": {
    "slack": {
      "tools": [
        "*"
      ],
      "type": "http",
      "url": "https://mcp.example.com/slack",
      "oauthClientId": "0oa1abcdEXAMPLE",
      "oauthPublicClient": true
    }
  }
}
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := mcpModule(t, []string{tt.provider}, map[string]config.MCPServer{"slack": oauthServer(tt.oauth)})
			m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
			changes := plan(t, m)
			wantTarget := "+ " + tt.provider + " mcp slack (http https://mcp.example.com/slack, then "
			if got := targets(changes); len(got) != 1 || !strings.HasPrefix(got[0], wantTarget) {
				t.Fatalf("plan = %q, want a create starting %q", got, wantTarget)
			}
			if len(m.Runner.(*runnertest.Fake).Calls) != 0 {
				t.Errorf("plan ran %v", m.Runner.(*runnertest.Fake).Lines())
			}
			apply(t, changes)
			if got := readFile(t, tt.file(m)); got != tt.want {
				t.Errorf("config =\n%s\nwant\n%s", got, tt.want)
			}
			assertConverged(t, m)

			m.Agents.MCP = nil
			apply(t, plan(t, m))
			assertConverged(t, m)
		})
	}
}

func TestMCPOAuthClientChangeUpdatesTheEntry(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex, config.ProviderCopilot}, map[string]config.MCPServer{
		"slack": oauthServer(config.MCPOAuth{ClientID: oauthClientID, CallbackPort: 54661}),
	})
	m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
	apply(t, plan(t, m))
	assertConverged(t, m)

	m.Agents.MCP["slack"] = oauthServer(config.MCPOAuth{ClientID: "0oa2otherEXAMPLE", CallbackPort: 54662, Scope: "read"})
	changes := plan(t, m)
	want := []string{
		"~ claude mcp slack (http https://mcp.example.com/slack, then sign in with /mcp in Claude Code)",
		"~ codex mcp slack (http https://mcp.example.com/slack, then sign in with `codex mcp login slack`)",
		"~ copilot mcp slack (http https://mcp.example.com/slack, then sign in with `/mcp auth slack` in Copilot CLI)",
	}
	if got := targets(changes); !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	if !strings.Contains(changes[0].Diff, `+    "clientId": "0oa2otherEXAMPLE",`) || !strings.Contains(changes[0].Diff, `-    "clientId": "0oa1abcdEXAMPLE"`) {
		t.Errorf("diff = %s", changes[0].Diff)
	}
	apply(t, changes)
	for path, want := range map[string]string{
		filepath.Join(m.Paths.Home, ".claude.json"):     `"callbackPort": 54662`,
		filepath.Join(m.CodexHome, "config.toml"):       "callback_port = 54662",
		filepath.Join(m.CopilotHome, "mcp-config.json"): `"redirectPort": 54662`,
	} {
		if got := readFile(t, path); !strings.Contains(got, want) || strings.Contains(got, "54661") {
			t.Errorf("%s lacks %s or keeps the old port:\n%s", path, want, got)
		}
	}
	assertConverged(t, m)

	m.Agents.MCP["slack"] = config.MCPServer{URL: "https://mcp.example.com/slack"}
	apply(t, plan(t, m))
	if got := readFile(t, filepath.Join(m.Paths.Home, ".claude.json")); strings.Contains(got, "oauth") {
		t.Errorf("claude keeps the oauth object:\n%s", got)
	}
	if got := readFile(t, filepath.Join(m.CodexHome, "config.toml")); strings.Contains(got, "oauth") || strings.Contains(got, "scopes") {
		t.Errorf("codex keeps the oauth table:\n%s", got)
	}
	assertConverged(t, m)
}

func TestMCPOAuthAdoptsWhatClaudeMcpAddWrote(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude}, map[string]config.MCPServer{
		"slack": oauthServer(config.MCPOAuth{ClientID: oauthClientID, CallbackPort: 54661}),
	})
	cfg := filepath.Join(m.Paths.Home, ".claude.json")
	written := `{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.com/slack","oauth":{"clientId":"0oa1abcdEXAMPLE","callbackPort":54661}}}}`
	writeFile(t, cfg, written)

	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ claude mcp slack (adopt the server already there)"}; !slices.Equal(got, want) {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	apply(t, changes)
	if readFile(t, cfg) != written {
		t.Error("adopting rewrote .claude.json")
	}
	assertConverged(t, m)
}

func TestMCPOAuthConflictsWithADifferingServer(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
		oauth    config.MCPOAuth
		path     func(m *Module) string
		existing string
	}{
		{
			"claude other client", config.ProviderClaude,
			config.MCPOAuth{ClientID: oauthClientID},
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.com/slack","oauth":{"clientId":"0oa9otherEXAMPLE"}}}}`,
		},
		{
			"claude without the client", config.ProviderClaude,
			config.MCPOAuth{ClientID: oauthClientID},
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.com/slack"}}}`,
		},
		{
			"claude extra port", config.ProviderClaude,
			config.MCPOAuth{ClientID: oauthClientID},
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.com/slack","oauth":{"clientId":"0oa1abcdEXAMPLE","callbackPort":1234}}}}`,
		},
		{
			"claude other scopes", config.ProviderClaude,
			config.MCPOAuth{ClientID: oauthClientID, Scope: "read"},
			func(m *Module) string { return filepath.Join(m.Paths.Home, ".claude.json") },
			`{"mcpServers":{"slack":{"type":"http","url":"https://mcp.example.com/slack","oauth":{"clientId":"0oa1abcdEXAMPLE","scopes":"write"}}}}`,
		},
		{
			"codex other client", config.ProviderCodex,
			config.MCPOAuth{ClientID: oauthClientID},
			func(m *Module) string { return filepath.Join(m.CodexHome, "config.toml") },
			"[mcp_servers.slack]\nurl = \"https://mcp.example.com/slack\"\n[mcp_servers.slack.oauth]\nclient_id = \"0oa9otherEXAMPLE\"\n",
		},
		{
			"copilot other client", config.ProviderCopilot,
			config.MCPOAuth{ClientID: oauthClientID},
			func(m *Module) string { return filepath.Join(m.CopilotHome, "mcp-config.json") },
			`{"mcpServers":{"slack":{"tools":["*"],"type":"http","url":"https://mcp.example.com/slack","oauthClientId":"0oa9otherEXAMPLE","oauthPublicClient":true}}}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := mcpModule(t, []string{tt.provider}, map[string]config.MCPServer{"slack": oauthServer(tt.oauth)})
			m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
			path := tt.path(m)
			writeFile(t, path, tt.existing)
			changes, err := planErr(t, m)
			if err == nil || !strings.Contains(err.Error(), `server "slack" is already in`) || !strings.Contains(err.Error(), "differs from zakwas.yaml in ") {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "0oa9otherEXAMPLE") {
				t.Errorf("error prints a value: %v", err)
			}
			if len(changes) != 0 || readFile(t, path) != tt.existing {
				t.Errorf("changes %v, file %q", targets(changes), readFile(t, path))
			}
		})
	}
}

func TestMCPOAuthTrueRegistersNothingExtra(t *testing.T) {
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex, config.ProviderCopilot}, map[string]config.MCPServer{
		"linear": oauthServer(config.MCPOAuth{Enabled: true}),
	})
	m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
	apply(t, plan(t, m))
	for _, path := range []string{filepath.Join(m.Paths.Home, ".claude.json"), filepath.Join(m.CodexHome, "config.toml"), filepath.Join(m.CopilotHome, "mcp-config.json")} {
		if got := readFile(t, path); strings.Contains(strings.ToLower(got), "oauth") || strings.Contains(got, "scopes") {
			t.Errorf("%s registers oauth settings:\n%s", path, got)
		}
	}
	assertConverged(t, m)
}

func TestGeminiOAuthClient(t *testing.T) {
	testutil.RequireSymlinks(t)
	m, _ := newGemini(t, config.Agents{MCP: map[string]config.MCPServer{
		"slack":  oauthServer(config.MCPOAuth{ClientID: oauthClientID, CallbackPort: 54661, Scope: "read write"}),
		"bare":   {URL: "https://mcp.example.com/bare", OAuth: config.MCPOAuth{ClientID: oauthClientID}},
		"hint":   {URL: "https://mcp.example.com/hint", OAuth: config.MCPOAuth{Enabled: true}},
		"dollar": {URL: "https://mcp.example.com/dollar", OAuth: config.MCPOAuth{ClientID: "id$HOME"}},
	}})
	apply(t, plan(t, m))
	manifest := readFile(t, filepath.Join(m.geminiExtDir(), "gemini-extension.json"))
	for _, want := range []string{
		`"slack": {
      "httpUrl": "https://mcp.example.com/slack",
      "oauth": {
        "enabled": true,
        "clientId": "0oa1abcdEXAMPLE",
        "scopes": [
          "read",
          "write"
        ],
        "redirectUri": "http://localhost:54661/oauth/callback"
      }
    }`,
		`"bare": {
      "httpUrl": "https://mcp.example.com/bare",
      "oauth": {
        "enabled": true,
        "clientId": "0oa1abcdEXAMPLE"
      }
    }`,
		`"hint": {
      "httpUrl": "https://mcp.example.com/hint"
    }`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest lacks\n%s\nin\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "dollar") {
		t.Errorf("manifest holds a client ID with a $, which Gemini would expand:\n%s", manifest)
	}
	warnings := strings.Join(m.Warnings(t.Context()), "\n")
	for _, want := range []string{
		"gemini leaves out mcp server dollar: Gemini would expand the $ in its command, args, url, env, headers or oauth",
		"gemini mcp server slack signs in with OAuth: run /mcp auth slack in Gemini CLI",
	} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings lack %q:\n%s", want, warnings)
		}
	}
	assertConverged(t, m)
}

const realisticClientID = "0oa1zlwt4wuQdH7xt358"

func TestCaptureReadsOAuthClients(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "slack": {"type": "http", "url": "https://mcp.example.com/slack", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "callbackPort": 54661, "scopes": "read write"}},
  "bare": {"type": "http", "url": "https://mcp.example.com/bare", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358"}},
  "long": {"type": "http", "url": "https://mcp.example.com/long", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt3580oa1zlwt4wuQdH7xt358"}},
  "secret": {"type": "http", "url": "https://mcp.example.com/secret", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "clientSecret": "LEAKO1"}},
  "snake": {"type": "http", "url": "https://mcp.example.com/snake", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "client_secret": "LEAKO2"}},
  "metadata": {"type": "http", "url": "https://mcp.example.com/metadata", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "authServerMetadataUrl": "https://auth.example.com/meta"}},
  "tokscope": {"type": "http", "url": "https://mcp.example.com/tokscope", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "scopes": "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"}},
  "badscope": {"type": "http", "url": "https://mcp.example.com/badscope", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358", "scopes": "read \"x\" LEAKO4"}},
  "spaced": {"type": "http", "url": "https://mcp.example.com/spaced", "oauth": {"clientId": "has space"}},
  "portonly": {"type": "http", "url": "https://mcp.example.com/portonly", "oauth": {"callbackPort": 54661}},
  "stdio": {"type": "stdio", "command": "srv", "oauth": {"clientId": "0oa1zlwt4wuQdH7xt358"}}}}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), `
[mcp_servers.cslack]
url = "https://mcp.example.com/cslack"
scopes = ["read", "write"]

[mcp_servers.cslack.oauth]
client_id = "0oa1zlwt4wuQdH7xt358"
callback_port = 54661

[mcp_servers.csecret]
url = "https://mcp.example.com/csecret"

[mcp_servers.csecret.oauth]
client_id = "0oa1zlwt4wuQdH7xt358"
client_secret = "LEAKO3"

[mcp_servers.curl]
url = "https://mcp.example.com/curl"

[mcp_servers.curl.oauth]
client_id = "0oa1zlwt4wuQdH7xt358"
callback_url = "http://localhost:54661/cb"
`)
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	if long := realisticClientID + realisticClientID; !tokenLike(long) {
		t.Fatalf("%q no longer looks like a token, so the long client ID below proves nothing", long)
	}
	got := capture(t, m)

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "LEAKO") {
		t.Fatalf("a secret reached the output: %s", data)
	}

	want := map[string]*CapturedOAuth{
		"slack":  {ClientID: realisticClientID, CallbackPort: 54661, Scope: "read write"},
		"bare":   {ClientID: realisticClientID},
		"long":   {ClientID: realisticClientID + realisticClientID},
		"cslack": {ClientID: realisticClientID, CallbackPort: 54661, Scope: "read write"},
	}
	for name, oauth := range want {
		c := find(t, got, KindMCP, name)
		if c.Skip != "" || c.Server == nil || !reflect.DeepEqual(c.Server.OAuth, oauth) {
			t.Errorf("%s: skip %q, server %+v, want oauth %+v", name, c.Skip, c.Server, oauth)
		}
	}
	for name, reason := range map[string]string{
		"secret":   "may hold a credential",
		"snake":    "may hold a credential",
		"csecret":  "may hold a credential",
		"metadata": "has settings capture can't declare (oauth)",
		"curl":     "has settings capture can't declare (oauth)",
		"tokscope": "its oauth scopes may hold a credential",
		"badscope": "its oauth settings can't be declared: oauth.scope must be visible ASCII scope names",
		"spaced":   "its oauth settings can't be declared: oauth.clientId may only hold visible",
		"portonly": "has settings capture can't declare (oauth)",
		"stdio":    "has settings capture can't declare (oauth)",
	} {
		c := find(t, got, KindMCP, name)
		if c.Server != nil || !strings.Contains(c.Skip, reason) {
			t.Errorf("%s: skip %q, server %+v, want a skip containing %q", name, c.Skip, c.Server, reason)
		}
	}
	if s := find(t, got, KindMCP, "slack").Server.Summary(); s != "http https://mcp.example.com/slack (oauth client "+realisticClientID+")" {
		t.Errorf("summary = %q", s)
	}

	m.Agents.MCP = map[string]config.MCPServer{}
	for _, name := range []string{"slack", "bare", "long", "cslack"} {
		server := find(t, got, KindMCP, name).Server.mcp()
		server.Providers = []string{config.ProviderClaude}
		if name == "cslack" {
			server.Providers = []string{config.ProviderCodex}
		}
		m.Agents.MCP[name] = server
	}
	changes := plan(t, m)
	want2 := []string{
		"~ claude mcp bare (adopt the server already there)", "~ claude mcp long (adopt the server already there)",
		"~ claude mcp slack (adopt the server already there)", "~ codex mcp cslack (adopt the server already there)",
	}
	if !slices.Equal(targets(changes), want2) {
		t.Fatalf("plan = %q, want %q", targets(changes), want2)
	}
	apply(t, changes)
	assertConverged(t, m)
}

func TestMCPOAuthQuotesInClientAndScope(t *testing.T) {
	testutil.RequireSymlinks(t)
	const client = `a'b"c\d`
	const scope = `it's:ok a'b`
	oauth := config.MCPOAuth{ClientID: client, CallbackPort: 1234, Scope: scope}
	m := mcpModule(t, []string{config.ProviderClaude, config.ProviderCodex, config.ProviderCopilot}, map[string]config.MCPServer{"slack": oauthServer(oauth)})
	m.CopilotHome = filepath.Join(m.Paths.Home, "copilot")
	apply(t, plan(t, m))
	assertConverged(t, m)
	for _, store := range []mcpStore{m.claudeMCP(), m.codexMCP(), m.copilotMCP()} {
		have, err := store.load(store.path())
		if err != nil {
			t.Fatal(err)
		}
		want, err := store.render("slack", oauthServer(oauth))
		if err != nil || have["slack"].canon != want.canon {
			t.Errorf("%s: read back %s, want %s (%v)", store.provider(), have["slack"].canon, want.canon, err)
		}
		if !strings.Contains(have["slack"].canon, `a'b`) {
			t.Errorf("%s lost the quote: %s", store.provider(), have["slack"].canon)
		}
	}

	g, _ := newGemini(t, config.Agents{MCP: map[string]config.MCPServer{"slack": oauthServer(oauth)}})
	apply(t, plan(t, g))
	var manifest geminiManifest
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(g.geminiExtDir(), "gemini-extension.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	got := manifest.MCPServers["slack"].OAuth
	if got == nil || got.ClientID != client || !slices.Equal(got.Scopes, []string{"it's:ok", "a'b"}) {
		t.Errorf("gemini oauth = %+v", got)
	}
	assertConverged(t, g)
}
