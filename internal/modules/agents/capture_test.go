package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func capture(t *testing.T, m *Module) []Candidate {
	t.Helper()
	got, err := m.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// listed is a candidate as "provider kind name", with "!" when it can't be
// declared.
func listed(cands []Candidate) []string {
	var out []string
	for _, c := range cands {
		s := c.Provider + " " + c.Kind + " " + c.Name
		if c.Skip != "" {
			s += " !"
		}
		out = append(out, s)
	}
	return out
}

func find(t *testing.T, cands []Candidate, kind, name string) Candidate {
	t.Helper()
	for _, c := range cands {
		if c.Kind == kind && c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s %q in %v", kind, name, listed(cands))
	return Candidate{}
}

func TestCaptureClaude(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "declared@sai"}},
		MCP:          map[string]config.MCPServer{"declared": {Command: "x"}},
		Permissions:  &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git log"}}},
	})
	c := m.claude()
	writeFile(t, c.settingsPath(), `{
  "extraKnownMarketplaces": {"sai": {}, "mine": {}, "other": {}},
  "permissions": {"allow": [
    "Bash(git log:*)", "Bash(git status:*)", "Bash(ls *)", "Bash(npm run build)",
    "WebFetch(domain:example.com)", "Bash(owned:*)", "Bash(git status:*)", "Bash"]}
}`)
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "declared": {"type": "stdio", "command": "x"},
  "owned": {"type": "stdio", "command": "owned"},
  "docs": {"type": "stdio", "command": "npx", "args": ["-y", "docs-mcp"], "env": {"DOCS_TOKEN": "${DOCS_TOKEN}"}},
  "remote": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${REMOTE_TOKEN}"}},
  "literal": {"type": "stdio", "command": "lit", "env": {"API_KEY": "hunter2"}},
  "keyed": {"type": "http", "url": "https://example.com/mcp?key=hunter2"}
}}`)
	writeFile(t, m.mcpStatePath(), `{"claude": {"`+m.claudeJSONPath()+`": {"owned": "abc"}}}`)
	writeFile(t, m.permissionsStatePath(), `{"claude": {"`+c.settingsPath()+`": {"allow": ["Bash(owned:*)"]}}}`)
	fake.OnOK(marketList, `[
		{"name":"sai","source":"github","repo":"o/sai"},
		{"name":"mine","source":"github","repo":"o/mine","ref":"v2"},
		{"name":"other","source":"git","url":"https://example.com/other.git"},
		{"name":"proj","source":"github","repo":"o/proj"}]`)
	fake.OnOK(pluginList, `{"installed": [
		{"id":"declared@sai","version":"1","scope":"user","enabled":true},
		{"id":"new@sai","version":"1","scope":"user","enabled":true},
		{"id":"off@sai","version":"1","scope":"user","enabled":false},
		{"id":"tool@mine","version":"1","scope":"user","enabled":true},
		{"id":"theirs@proj","version":"1","scope":"user","enabled":true},
		{"id":"team@sai","version":"1","scope":"project","enabled":true},
		{"id":"local@sai","version":"1","scope":"local","enabled":true},
		{"id":"skill@skills-dir","version":"1","scope":"user","enabled":true},
		{"id":"cloud@synced","version":"1","scope":"user","enabled":true},
		{"id":"core@builtin","version":"1","scope":"user","enabled":true},
		{"id":"dir@inline","version":"1","scope":"user","enabled":true}], "available": []}`)

	got := capture(t, m)
	want := []string{
		"claude marketplace mine",
		"claude marketplace other",
		"claude plugin new@sai",
		"claude plugin off@sai !",
		"claude plugin theirs@proj !",
		"claude plugin tool@mine",
		"claude mcp docs",
		"claude mcp keyed !",
		"claude mcp literal !",
		"claude mcp remote",
		"claude permission Bash(...) !",
		"claude permission Bash(...) !",
		"claude permission WebFetch(...) !",
		"claude permission git status",
		"claude permission ls",
	}
	if l := listed(got); !slices.Equal(l, want) {
		t.Errorf("candidates\n got %q\nwant %q", l, want)
	}
	assertReadOnly(t, fake)

	if mk := find(t, got, KindMarketplace, "mine"); mk.Source != "o/mine#v2" || mk.Provider != config.ProviderClaude {
		t.Errorf("marketplace = %+v", mk)
	}
	if s := find(t, got, KindMCP, "docs").Server; !reflect.DeepEqual(s, &CapturedServer{Command: "npx", Args: []string{"-y", "docs-mcp"}, EnvVars: []string{"DOCS_TOKEN"}}) {
		t.Errorf("docs = %+v", s)
	}
	if s := find(t, got, KindMCP, "remote").Server; !reflect.DeepEqual(s, &CapturedServer{URL: "https://example.com/mcp", BearerTokenEnv: "REMOTE_TOKEN"}) {
		t.Errorf("remote = %+v", s)
	}
	if why := find(t, got, KindMCP, "literal").Skip; !strings.Contains(why, "env") || !strings.Contains(why, "by hand") {
		t.Errorf("literal env: %q", why)
	}
	if why := find(t, got, KindMCP, "keyed").Skip; !strings.Contains(why, "may hold a credential") {
		t.Errorf("keyed URL: %q", why)
	}
	if p := find(t, got, KindPermission, "git status"); p.Decision != config.PermissionAllow || p.Found != m.Paths.Pretty(c.settingsPath()) {
		t.Errorf("rule = %+v", p)
	}
	if why := find(t, got, KindPlugin, "theirs@proj").Skip; !strings.Contains(why, `"proj"`) {
		t.Errorf("plugin of an undeclared marketplace: %q", why)
	}
}

func TestCaptureLeavesNothingWhenEverythingIsDeclared(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "a@sai"}},
		MCP:          map[string]config.MCPServer{"docs": {Command: "npx"}},
		Permissions:  &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git status"}}},
	})
	writeFile(t, m.claude().settingsPath(), `{"extraKnownMarketplaces": {"sai": {}}, "permissions": {"allow": ["Bash(git status *)"]}}`)
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {"docs": {"type": "stdio", "command": "other"}}}`)
	fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai"}]`)
	fake.OnOK(pluginList, `{"installed": [{"id":"a@sai","version":"1","scope":"user","enabled":true}], "available": []}`)
	if got := capture(t, m); len(got) != 0 {
		t.Errorf("candidates = %v", listed(got))
	}
}

func TestCaptureCodex(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "a@sai"}},
		Permissions:  &config.AgentPermissions{Allow: []config.AgentPermission{{Command: "git log"}}},
	})
	home := m.CodexHome
	writeFile(t, filepath.Join(home, "config.toml"), `
[marketplaces.sai]
source_type = "git"
source = "o/sai"

[marketplaces.cx]
source_type = "git"
source = "o/cx"
ref = "v1"

[marketplaces.dir]
source_type = "local"
source = "/work/mp"

[mcp_servers.docs]
command = "npx"
args = ["-y", "docs-mcp"]
env_vars = ["DOCS_TOKEN"]

[mcp_servers.remote]
url = "https://example.com/mcp"
bearer_token_env_var = "REMOTE_TOKEN"

[mcp_servers.tuned]
command = "tuned"
startup_timeout_sec = 30
`)
	writeFile(t, filepath.Join(home, "rules", "default.rules"), `# approvals
prefix_rule(pattern=["git", "status"], decision="allow")
prefix_rule(pattern=["git", "log"], decision="allow")
prefix_rule(pattern=["git", "status"], decision="allow")
prefix_rule(pattern=["rm", "-rf"], decision="forbidden", justification="never \"rm\"")
prefix_rule(pattern=["cargo", ["build", "test"]], decision="allow")
prefix_rule(pattern=["make"], decision="sometimes")
`)
	writeFile(t, filepath.Join(home, "rules", "zakwas.rules"), `prefix_rule(pattern=["owned"], decision="allow")`+"\n")
	fake.OnOK(codexMarketList, `{"marketplaces":[{"name":"sai","root":"/x"},{"name":"cx","root":"/y"},{"name":"home","root":"/z"}]}`)
	fake.OnOK(codexPluginList, `{"installed": [
		{"pluginId":"a@sai","version":"1","enabled":true},
		{"pluginId":"p@cx","version":"1","enabled":true},
		{"pluginId":"q@cx","version":"1","enabled":false},
		{"pluginId":"mine@home","version":"1","enabled":true}], "available": []}`)

	got := capture(t, m)
	want := []string{
		"codex marketplace cx",
		"codex marketplace dir",
		"codex plugin p@cx",
		"codex plugin q@cx !",
		"codex mcp docs",
		"codex mcp remote",
		"codex mcp tuned !",
		"codex permission git status",
		"codex permission prefix_rule on line 6 !",
		"codex permission prefix_rule on line 7 !",
		"codex permission rm -rf",
	}
	if l := listed(got); !slices.Equal(l, want) {
		t.Errorf("candidates\n got %q\nwant %q", l, want)
	}
	assertCodexReadOnly(t, fake)
	assertCodexCalls(t, m, fake)

	if mk := find(t, got, KindMarketplace, "cx"); mk.Source != "o/cx#v1" {
		t.Errorf("marketplace = %+v", mk)
	}
	if s := find(t, got, KindMCP, "remote").Server; !reflect.DeepEqual(s, &CapturedServer{URL: "https://example.com/mcp", BearerTokenEnv: "REMOTE_TOKEN"}) {
		t.Errorf("remote = %+v", s)
	}
	if why := find(t, got, KindMCP, "tuned").Skip; !strings.Contains(why, "startup_timeout_sec") {
		t.Errorf("tuned: %q", why)
	}
	if p := find(t, got, KindPermission, "rm -rf"); p.Decision != config.PermissionDeny {
		t.Errorf("rule = %+v", p)
	}
	if p := find(t, got, KindPermission, "git status"); p.Decision != config.PermissionAllow {
		t.Errorf("rule = %+v", p)
	}
}

func TestCaptureOnlyReadsManagedProviders(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderOpencode}})
	if got := capture(t, m); len(got) != 0 || len(fake.Calls) != 0 {
		t.Errorf("candidates %v, ran %v", listed(got), fake.Lines())
	}

	m, fake = newModule(t, config.Agents{Providers: []string{config.ProviderClaude}})
	writeFile(t, filepath.Join(m.codexBackend().home, "rules", "default.rules"), `prefix_rule(pattern=["ls"], decision="allow")`+"\n")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	if got := capture(t, m); len(got) != 0 {
		t.Errorf("a provider that isn't managed was read: %v", listed(got))
	}

	m, fake = newModule(t, config.Agents{Providers: nil})
	m.Agents.Providers = []string{config.ProviderClaude, config.ProviderOpencode}
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	if got := capture(t, m); len(got) != 0 {
		t.Errorf("candidates = %v", listed(got))
	}
	for _, l := range fake.Lines() {
		if strings.HasPrefix(l, "codex") {
			t.Errorf("ran %q although codex is opt-in", l)
		}
	}
}

func TestCaptureReportsAProviderThatFailsAlone(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	fake.Missing("claude")
	writeFile(t, filepath.Join(m.codexBackend().home, "rules", "default.rules"), `prefix_rule(pattern=["ls"], decision="allow")`+"\n")
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), "")
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	got, err := m.Capture(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: Claude Code is not installed") {
		t.Errorf("error = %v", err)
	}
	if l := listed(got); !slices.Equal(l, []string{"codex permission ls"}) {
		t.Errorf("candidates = %q", l)
	}
}

func TestCaptureNeverChangesAnything(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	settings := `{"permissions": {"allow": ["Bash(ls:*)"]}, "extraKnownMarketplaces": {"x": {}}}`
	writeFile(t, m.claude().settingsPath(), settings)
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {"a": {"type": "stdio", "command": "a"}}}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), "[mcp_servers.b]\ncommand = \"b\"\n")
	writeFile(t, filepath.Join(m.codexBackend().home, "rules", "default.rules"), `prefix_rule(pattern=["ls"], decision="allow")`+"\n")
	fake.OnOK(marketList, `[{"name":"x","source":"github","repo":"o/x"}]`)
	fake.OnOK(pluginList, `{"installed": [{"id":"p@x","version":"1","scope":"user","enabled":true}], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	files := []string{m.claude().settingsPath(), m.claudeJSONPath(), filepath.Join(m.codexBackend().home, "config.toml"), filepath.Join(m.codexBackend().home, "rules", "default.rules")}
	before := make([]string, len(files))
	for i, f := range files {
		before[i] = readFile(t, f)
	}
	capture(t, m)
	for i, f := range files {
		if readFile(t, f) != before[i] {
			t.Errorf("%s changed", f)
		}
	}
	for _, l := range fake.Lines() {
		if !slices.Contains([]string{marketList, pluginList, codexMarketList, codexPluginList}, l) {
			t.Errorf("capture ran %q", l)
		}
	}
	if _, err := json.Marshal(capture(t, m)); err != nil {
		t.Error(err)
	}
}

func TestClaudeBashPrefix(t *testing.T) {
	tests := []struct {
		rule, want, why string
	}{
		{"Bash(git status:*)", "git status", ""},
		{"Bash(git   status *)", "git status", ""},
		{"Bash(npm run build)", "", "exact"},
		{"Bash(*)", "", "every command"},
		{"Bash(:*)", "", "every command"},
		{"Bash", "", "every command"},
		{"Read(~/x)", "", "not a Bash"},
		{"WebFetch(domain:x.com)", "", "not a Bash"},
	}
	for _, tt := range tests {
		got, why := claudeBashPrefix(tt.rule)
		if got != tt.want || !strings.Contains(why, tt.why) {
			t.Errorf("%q = %q, %q; want %q, %q", tt.rule, got, why, tt.want, tt.why)
		}
	}
}

func TestParsePrefixRule(t *testing.T) {
	tests := []struct {
		line          string
		words         []string
		decision, why string
	}{
		{`prefix_rule(pattern=["git", "status"], decision="allow")`, []string{"git", "status"}, "allow", ""},
		{`prefix_rule(pattern = ["ls"])`, []string{"ls"}, "allow", ""},
		{`prefix_rule(pattern=["git", "push"], decision="prompt", justification="ask first")`, []string{"git", "push"}, "review", ""},
		{`prefix_rule(pattern=["a b", "c\\d"], decision="forbidden")`, []string{"a b", `c\d`}, "deny", ""},
		{`prefix_rule(pattern=["cargo", ["build", "test"]], decision="allow")`, nil, "", "alternatives"},
		{`prefix_rule(pattern=[], decision="allow")`, nil, "", "empty"},
		{`prefix_rule(decision="allow")`, nil, "", "alternatives"},
		{`prefix_rule(pattern=["x"], decision="maybe")`, []string{"x"}, "", "not one zakwas declares"},
	}
	for _, tt := range tests {
		words, decision, why := parsePrefixRule(tt.line)
		if !slices.Equal(words, tt.words) || decision != tt.decision || !strings.Contains(why, tt.why) || (tt.why == "") != (why == "") {
			t.Errorf("%s\n got %q %q %q\nwant %q %q %q", tt.line, words, decision, why, tt.words, tt.decision, tt.why)
		}
	}
}

func TestSameSource(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		same bool
	}{
		{"o/sai", "https://github.com/o/sai.git", true},
		{"o/sai", "O/SAI", true},
		{"o/sai#v1", "o/sai", false},
		{"o/sai", "o/other", false},
	} {
		if got := SameSource(tt.a, tt.b); got != tt.same {
			t.Errorf("SameSource(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestCaptureKeepsCredentialsOutOfTheFindings(t *testing.T) {
	m, fake := newModule(t, config.Agents{})
	writeFile(t, m.claude().settingsPath(), `{"extraKnownMarketplaces": {"priv": {}, "web": {}, "ssh": {}, "plain": {}}}`)
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "flag": {"type": "stdio", "command": "gh", "args": ["-y", "gh-mcp", "--token=ghp_ARGSECRET"]},
  "split": {"type": "stdio", "command": "gh", "args": ["--api-key", "abc"]},
  "prefix": {"type": "stdio", "command": "gh", "args": ["ghp_ABCDEFGH"]},
  "fine": {"type": "stdio", "command": "gh", "args": ["--tokens-per-minute-limit-disabled"]}
}}`)
	fake.OnOK(marketList, `[
		{"name":"priv","source":"git","url":"https://x-access-token:ghp_SECRET123@github.com/o/priv.git"},
		{"name":"web","source":"git","url":"https://example.com/m.json?token=ABC"},
		{"name":"ssh","source":"git","url":"ssh://git@github.com/o/ssh.git"},
		{"name":"plain","source":"github","repo":"o/plain","ref":"v1"}]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	for _, name := range []string{"priv", "web"} {
		if why := find(t, got, KindMarketplace, name).Skip; !strings.Contains(why, "may hold a credential") {
			t.Errorf("%s: %q", name, why)
		}
	}
	for _, name := range []string{"ssh", "plain"} {
		if c := find(t, got, KindMarketplace, name); c.Skip != "" {
			t.Errorf("%s skipped: %q", name, c.Skip)
		}
	}
	for _, name := range []string{"flag", "split", "prefix"} {
		c := find(t, got, KindMCP, name)
		if !strings.Contains(c.Skip, "credential") || c.Server != nil {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if c := find(t, got, KindMCP, "fine"); c.Server == nil {
		t.Errorf("an ordinary argument was refused: %q", c.Skip)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SECRET123", "token=ABC", "ARGSECRET"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("%s reached the output: %s", secret, data)
		}
	}
}

func TestCaptureReadsCodexRulesOnlyWhenTheyAreWholeAndKeepsTheRestOnASymlink(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{})
	real := filepath.Join(t.TempDir(), "dotfiles", "default.rules")
	writeFile(t, real, "prefix_rule(pattern=[\"dd\"],\n  decision=\"forbidden\")\n"+
		"prefix_rule(pattern=[\"rm\", \"-rf\"], decision='forbidden')\n"+
		"prefix_rule(pattern=[\"ls\"], decision=\"allow\")\n")
	link := filepath.Join(m.CodexHome, "rules", "default.rules")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m.CodexHome, "config.toml"), "[marketplaces.cx]\nsource_type = \"git\"\nsource = \"o/cx\"\n")
	fake.OnOK(codexMarketList, `{"marketplaces":[{"name":"cx","root":"/y"}]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	want := []string{
		"codex marketplace cx",
		`codex permission ls`,
		"codex permission prefix_rule on line 1 !",
		"codex permission prefix_rule on line 3 !",
	}
	if l := listed(got); !slices.Equal(l, want) {
		t.Errorf("candidates\n got %q\nwant %q", l, want)
	}
}

func TestCaptureServerDeclaredForOtherProvidersIsReported(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		MCP:       map[string]config.MCPServer{"docs": {Command: "npx", Providers: []string{config.ProviderCodex}}},
	})
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {"docs": {"type": "stdio", "command": "other"}}}`)
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), "[mcp_servers.docs]\ncommand = \"npx\"\n")
	got := capture(t, m)
	if l := listed(got); !slices.Equal(l, []string{"claude mcp docs !"}) || !strings.Contains(got[0].Skip, "other providers only") {
		t.Errorf("candidates = %q", l)
	}
}

func TestLooksSecret(t *testing.T) {
	secret := [][]string{
		{"curl", "-H", "Authorization:Bearer-ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"export", "GITHUB_TOKEN=hunter2secret"},
		{"aws", "--secret-access-key", "X", "s3", "ls"},
		{"mysql", "-pPASS1234"},
		{"curl", "-H", "Authorization: Bearer sk-abcdefghSECRET"},
		{"gh", "auth", "login", "--with-token", "ghp_x"},
		{"mcp-remote", "https://x.example.com", "--header", "Authorization: Bearer X"},
		{"--auth=X"},
		{"--api_key", "X"},
		{"--key", "X"},
		{"--access-key", "X"},
		{"postgres://user:pass@host/db"},
		{"--dsn=mysql://u:p@h/db"},
		{"-c", "API_KEY=X srv"},
		{"login", "X"},
		{"--url", "https://x/mcp?key=abc"},
		{"abcdefghij0123456789abcdefgh"},
		{"sshpass", "-p", "VALUE", "ssh"},
		{"setx", "API_KEY", "VALUE"},
		{"docker", "login", "-p", "VALUE"},
		{"foo", "-k", "VALUE"},
		{"foo", "--pw", "VALUE"},
		{"foo", "--passphrase", "VALUE"},
		{"foo", "--pin", "VALUE"},
		{"foo", "--session-id", "VALUE"},
		{"curl", "-u", "admin:VALUE", "https://x"},
		{"curl", "--proxy-user", "a:VALUE"},
		{"foo", "--userpass", "a:VALUE"},
		{"aws", "configure", "set", "aws_secret_access_key", "VALUE"},
		{"export", "NAME_TOKEN", "VALUE"},
		{"npm", "config", "set", "//registry.npmjs.org/:_authToken", "VALUE"},
		{"openssl", "enc", "-pass", "pass:VALUE"},
		{"security", "add-generic-password", "-w", "VALUE"},
		{"net", "use", "/password:VALUE"},
		{"foo", "--token:VALUE"},
		{"curl", "--header=Authorization:VALUE"},
		{"--connection-string", "Server=h;Password=VALUE;"},
		{"--connection-string", "Host=h;Pwd=VALUE"},
		{"--config", `{"apiKey": "VALUE"}`},
		{"git", "clone", "https://VALUE@github.com/o/r"},
		{"https://host/mcp#VALUE"},
		{"foo", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N"},
		{"foo", "Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MA=="},
		{"foo", "sk_live_abcdefghijklmnop"},
		{"foo", "rk_live_abcdefghijklmnop"},
		{"powershell", "-Command", "$env:TOKEN='VALUE'"},
		{"curl", "https://hooks.slack.com/services/T0000/B0000/XXXXXXXX"},
		{"foo", "--cookie", "session=VALUE"},
		{"foo", "123e4567-e89b-12d3-a456-426614174000"},
		{"-e", "DB_PASS=VALUE"},
		{"foo", "PATTOKEN=VALUE"},
	}
	for _, words := range secret {
		if !looksSecret(words) {
			t.Errorf("%q isn't recognized", words)
		}
	}
	plain := [][]string{
		{"git", "status"},
		{"gh", "auth", "status"},
		{"gh", "auth", "login"},
		{"npx", "-y", "@scope/task-manager-mcp"},
		{"--max-tokens-disabled"},
		{"disk-mcp"},
		{"https://mcp.example.com/mcp"},
		{"https://mcp.example.com/v1/sse"},
		{"npm", "run", "build"},
		{"--stdio"},
		{"mkdir", "-p"},
		{"ls", "-t"},
		{"gh", "api", "-H", "Accept: application/json"},
		{"git", "clone", "ssh://git@github.com/o/r.git"},
		{"PATH=/usr/bin", "ls"},
		{"--path=/tmp/x"},
		{"/usr/local/bin/some-tool", "--flag"},
		{"docker", "run", "--rm", "-it", "alpine"},
		{"cargo", "test", "--workspace"},
	}
	for _, words := range plain {
		if looksSecret(words) {
			t.Errorf("%q is refused", words)
		}
	}
}

func TestCaptureScreensRulesAndApprovalsAndNeverPrintsTheirText(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	writeFile(t, m.claude().settingsPath(), `{"permissions": {"allow": [
  "Bash(curl -H Authorization:Bearer-ghp_abcdefghijklmnopqrstuvwxyz0123456789:*)",
  "Bash(export GITHUB_TOKEN=hunter2secret:*)",
  "Bash(mysql -pLEAKR3:*)",
  "Bash(aws --secret-access-key LEAKR4 s3 ls:*)",
  "Read(/home/me/LEAKR5/**)",
  "Bash(git status:*)"]}}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "rules", "default.rules"),
		"prefix_rule(pattern=[\"curl\", \"-H\", \"Authorization: Bearer sk-LEAKC5abcdef\"], decision=\"allow\")\n"+
			"prefix_rule(pattern=[\"ls\"], decision=\"allow\", justification=\"token=LEAKC6\")\n"+
			"prefix_rule(pattern=[\"git\", \"log\"], decision=\"allow\")\n")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	if l := listed(got); !slices.Equal(l, []string{
		"claude permission Bash(...) !", "claude permission Bash(...) !", "claude permission Bash(...) !", "claude permission Bash(...) !",
		"claude permission Read(...) !", "claude permission git status",
		"codex permission git log", "codex permission ls", "codex permission prefix_rule on line 1 !",
	}) {
		t.Errorf("candidates = %q", l)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"ghp_", "hunter2", "LEAKR", "LEAKC", "sk-"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("%s reached the output: %s", leak, data)
		}
	}
	if why := got[0].Skip; !strings.Contains(why, "credential") {
		t.Errorf("reason = %q", why)
	}
}

func TestCaptureScreensMCPServers(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "hdr": {"type": "stdio", "command": "npx", "args": ["mcp-remote", "https://x.example.com", "--header", "Authorization: Bearer LEAKM1"]},
  "auth": {"type": "stdio", "command": "srv", "args": ["--auth=LEAKM2"]},
  "pathkey": {"type": "http", "url": "https://mcp.example.com/LEAKM3/mcp"},
  "pos": {"type": "stdio", "command": "srv", "args": ["login", "LEAKM4"]},
  "dsn": {"type": "stdio", "command": "srv", "args": ["postgres://user:LEAKM5@host/db"]},
  "sh": {"type": "stdio", "command": "sh", "args": ["-c", "API_KEY=LEAKM6 srv"]},
  "ok": {"type": "http", "url": "https://mcp.example.com/mcp"}}}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), "[mcp_servers.chdr]\ncommand = \"npx\"\nargs = [\"--header\", \"Authorization: Bearer LEAKC2\"]\n")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "LEAK") {
		t.Errorf("a secret reached the output: %s", data)
	}
	for _, c := range got {
		if (c.Name == "ok") == (c.Skip != "") {
			t.Errorf("%+v", c)
		}
	}
	if len(got) != 8 {
		t.Errorf("candidates = %q", listed(got))
	}
}

func TestCaptureNeverEchoesAValueInAReason(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderClaude, config.ProviderCodex}})
	writeFile(t, m.claude().settingsPath(), `{"permissions": {"allow": [
  "Bash(curl -d {\"password\":\"LEAKR5\"}:*)"]}}`)
	writeFile(t, m.claudeJSONPath(), `{"mcpServers": {
  "name": {"type": "stdio", "command": "srv", "env": {"API_KEY": "LEAKR6"}},
  "sec": {"type": "http", "url": "https://h.example.com/mcp", "headers": {"Authorization": "Bearer LEAKR7"}},
  "env": {"type": "stdio", "command": "srv", "env": {"lowercase": "${lowercase}"}},
  "tok": {"type": "http", "url": "https://h.example.com/mcp", "headers": {"Authorization": "Bearer ${ghp_Zq8xK2mP9vL4tR7wN3bY6cD1fG5hJ0sA}"}}}}`)
	writeFile(t, filepath.Join(m.codexBackend().home, "config.toml"), `
[marketplaces.mk]
source_type = "git"
source = "o/r"
ref = "ghp_Zq8xK2mP9vL4tR7wN3bY6cD1fG5hJ0sA"

[mcp_servers.web]
url = "https://h.example.com/mcp"
bearer_token_env_var = "ghp_Zq8xK2mP9vL4tR7wN3bY6cD1fG5hJ0sA"

[mcp_servers.low]
url = "https://h.example.com/mcp"
bearer_token_env_var = "leakr8"

[mcp_servers.vars]
command = "srv"
env_vars = ["API_KEY=LEAKR9"]
`)
	writeFile(t, filepath.Join(m.codexBackend().home, "rules", "default.rules"),
		"prefix_rule(pattern=[\"sshpass\", \"-p\", \"LEAKR10\", \"ssh\"], decision=\"allow\")\n"+
			"prefix_rule(pattern=[\"setx\", \"API_KEY\", \"LEAKR11\"], decision=\"allow\")\n"+
			"prefix_rule(pattern=[\"ls\"], decision=\"allow\", justification=\"the key is LEAKR12\")\n"+
			"prefix_rule(pattern=[\"x\"], decision=\"maybe-LEAKR13\")\n")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces":[{"name":"mk","root":"/x"}]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	got := capture(t, m)
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"LEAKR", "leakr8", "ghp_", "hunter2"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("%s reached the output: %s", leak, data)
		}
	}
	ls := find(t, got, KindPermission, "ls")
	if ls.Skip != "" {
		t.Errorf("an ordinary rule with a free-text justification was skipped: %q", ls.Skip)
	}
	if strings.Contains(string(data), "justification") {
		t.Errorf("a justification reached the findings: %s", data)
	}
}
