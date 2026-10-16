package proposal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/testutil"
)

const baseConfig = `# my setup
protect:
  immutable: false
commands:
  - name: motd
    check: test -e ~/.motd
    run: >-
      echo hello
        indented line
      done
brew:
  file: Brewfile
mise:
  config: mise/config.toml
agents:
  providers: [claude]
  marketplaces:
    market: owner/repo
  plugins:
    - first@market
  permissions:
    allow:
      - git status
      - ls
    deny:
      - rm
`

const baseBrewfile = "brew \"git\"\ncask \"firefox\" # browser\nbrew 'single'\n"

const baseMise = "# tools\n[settings]\nexperimental = true\n\n[tools]\nnode = \"22.1.0\" # lts\ngo = \"1.27\"\n\n[env]\nA = \"b\"\n"

func newRepo(t *testing.T, yamlText string) (*config.Config, string) {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("zakwas.yaml", yamlText)
	write("Brewfile", baseBrewfile)
	write("mise/config.toml", baseMise)
	cfg, err := config.LoadFor(filepath.Join(root, "zakwas.yaml"), home, platform.Darwin)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, home
}

func TestApplyEditsOnlyTheEntries(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	res, err := Apply([]Op{
		{Kind: AddPlugin, Name: "second@market", Providers: []string{"claude"}},
		{Kind: AddPermission, Decision: "deny", Command: "git  push --force", Justification: "use --force-with-lease"},
		{Kind: RemovePermission, Decision: "allow", Command: "ls"},
		{Kind: AddMCP, Name: "docs", Server: &Server{Command: "npx", Args: []string{"-y", "docs-mcp"}, EnvVars: []string{"DOCS_TOKEN"}}},
	}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edits) != 1 || res.Edits[0].Rel != "zakwas.yaml" {
		t.Fatalf("edits = %+v", res.Edits)
	}
	want := strings.Replace(baseConfig, "    - first@market\n", "    - first@market\n    - id: second@market\n      providers: [claude]\n", 1)
	want = strings.Replace(want, "      - ls\n", "", 1)
	want = strings.Replace(want, "      - rm\n", "      - rm\n      - command: git push --force\n        justification: use --force-with-lease\n", 1)
	want += "  mcp:\n    docs:\n      command: npx\n      args: [\"-y\", docs-mcp]\n      envVars: [DOCS_TOKEN]\n"
	if got := string(res.Edits[0].New); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, ok := res.Config.Agents.MCP["docs"]; !ok || len(res.Config.Agents.Plugins) != 2 {
		t.Errorf("parsed config doesn't have the edits: %+v", res.Config.Agents)
	}
	onDisk, _ := os.ReadFile(cfg.Path)
	if string(onDisk) != baseConfig {
		t.Error("Apply changed zakwas.yaml on disk")
	}
}

func TestApplyMCPServerWithOAuthClient(t *testing.T) {
	for _, tt := range []struct {
		name  string
		oauth *OAuth
		want  string
	}{
		{"full", &OAuth{ClientID: "0oa1abcdEXAMPLE", CallbackPort: intp(54661), Scope: "read write"}, "      oauth: {clientId: 0oa1abcdEXAMPLE, callbackPort: 54661, scope: read write}\n"},
		{"client only", &OAuth{ClientID: "0oa1abcdEXAMPLE"}, "      oauth: {clientId: 0oa1abcdEXAMPLE}\n"},
		{"client id that reads as a number", &OAuth{ClientID: "12345"}, "      oauth: {clientId: \"12345\"}\n"},
		{"scope with a colon", &OAuth{ClientID: "c", Scope: "api:read api:write"}, "      oauth: {clientId: c, scope: api:read api:write}\n"},
		{"client id with a flow character", &OAuth{ClientID: "a,b"}, "      oauth: {clientId: \"a,b\"}\n"},
		{"client id with a question mark", &OAuth{ClientID: "a?b"}, "      oauth: {clientId: \"a?b\"}\n"},
		{"quotes in client id and scope", &OAuth{ClientID: `a'b"c\d`, Scope: "it's:ok a'b"}, `      oauth: {clientId: a'b"c\d, scope: it's:ok a'b}` + "\n"},
		{"client id that is a merge key", &OAuth{ClientID: "<<"}, "      oauth: {clientId: \"<<\"}\n"},
		{"scope that is a merge key", &OAuth{ClientID: "c", Scope: "<<"}, "      oauth: {clientId: c, scope: \"<<\"}\n"},
		{"client id that is a tilde", &OAuth{ClientID: "~"}, "      oauth: {clientId: \"~\"}\n"},
		{"client id that is null", &OAuth{ClientID: "null"}, "      oauth: {clientId: \"null\"}\n"},
		{"client id that is a date", &OAuth{ClientID: "2001-01-01"}, "      oauth: {clientId: \"2001-01-01\"}\n"},
		{"client id that is a boolean", &OAuth{ClientID: "true", Scope: "off"}, "      oauth: {clientId: \"true\", scope: \"off\"}\n"},
		{"client id that is an anchor", &OAuth{ClientID: "&a"}, "      oauth: {clientId: \"&a\"}\n"},
		{"client id that is an alias", &OAuth{ClientID: "*a"}, "      oauth: {clientId: \"*a\"}\n"},
		{"scope with a question mark", &OAuth{ClientID: "c", Scope: "https://x/y?z"}, "      oauth: {clientId: c, scope: \"https://x/y?z\"}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, home := newRepo(t, baseConfig)
			res, err := Apply([]Op{
				{Kind: AddMCP, Name: "slack", Providers: []string{"claude"}, Server: &Server{URL: "https://mcp.example.com/slack", OAuth: tt.oauth}},
			}, cfg, home)
			if err != nil {
				t.Fatal(err)
			}
			want := baseConfig + "  mcp:\n    slack:\n      url: https://mcp.example.com/slack\n" + tt.want + "      providers: [claude]\n"
			if got := string(res.Edits[0].New); got != want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, want)
			}
			got := res.Config.Agents.MCP["slack"].OAuth
			if got.ClientID != tt.oauth.ClientID || got.CallbackPort != derefPort(tt.oauth.CallbackPort) || got.Scope != tt.oauth.Scope || !got.On() {
				t.Errorf("parsed oauth = %+v, want %+v", got, tt.oauth)
			}
		})
	}
}

func TestValidateMCPOAuthClient(t *testing.T) {
	url := func(o *OAuth) Op {
		return Op{Kind: AddMCP, Name: "a", Server: &Server{URL: "https://e.com", OAuth: o}}
	}
	for _, tt := range []struct {
		name string
		op   Op
		want string
	}{
		{"valid", url(&OAuth{ClientID: "c", CallbackPort: intp(1), Scope: "a b"}), ""},
		{"no client", url(&OAuth{CallbackPort: intp(1)}), "oauth needs a client id"},
		{"empty", url(&OAuth{}), "oauth needs a client id"},
		{"command server", Op{Kind: AddMCP, Name: "a", Server: &Server{Command: "x", OAuth: &OAuth{ClientID: "c"}}}, "oauth belongs to a url server"},
		{"bearer token", Op{Kind: AddMCP, Name: "a", Server: &Server{URL: "https://e.com", BearerTokenEnv: "T", OAuth: &OAuth{ClientID: "c"}}}, "can't be combined with a bearer token"},
		{"negative port", url(&OAuth{ClientID: "c", CallbackPort: intp(-1)}), "between 1 and 65535"},
		{"port too high", url(&OAuth{ClientID: "c", CallbackPort: intp(65536)}), "between 1 and 65535"},
		{"control character", url(&OAuth{ClientID: "c\x1b[2J"}), "may only hold visible"},
		{"port zero", url(&OAuth{ClientID: "c", CallbackPort: intp(0)}), "between 1 and 65535"},
		{"scope", url(&OAuth{ClientID: "c", Scope: "a  b"}), "oauth.scope"},
		{"right-to-left override", url(&OAuth{ClientID: "ab\u202ecd"}), "may only hold visible"},
		{"zero-width space", url(&OAuth{ClientID: "ab\u200bcd"}), "may only hold visible"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.op.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("err = %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApplyMarketplaceThenItsPlugin(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	_, err := Apply([]Op{
		{Kind: AddPlugin, Name: "tool@fresh", Providers: []string{"claude"}},
		{Kind: AddMarketplace, Name: "fresh", Source: "o/fresh#v2", Providers: []string{"claude"}},
		{Kind: AddMarketplace, Name: "local", Source: "~/mp"},
	}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), `marketplace "fresh" is not declared`) {
		t.Fatalf("a plugin before its marketplace: %v", err)
	}
	res, err := Apply([]Op{
		{Kind: AddMarketplace, Name: "fresh", Source: "o/fresh#v2", Providers: []string{"claude"}},
		{Kind: AddMarketplace, Name: "local", Source: "~/mp"},
		{Kind: AddPlugin, Name: "tool@fresh", Providers: []string{"claude"}},
	}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(baseConfig, "    market: owner/repo\n", "    market: owner/repo\n    fresh:\n      source: o/fresh#v2\n      providers: [claude]\n    local:\n      source: ~/mp\n", 1)
	want = strings.Replace(want, "    - first@market\n", "    - first@market\n    - id: tool@fresh\n      providers: [claude]\n", 1)
	if got := string(res.Edits[0].New); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if m := res.Config.Agents.Marketplaces["fresh"]; m.Source != "o/fresh#v2" {
		t.Errorf("parsed marketplace = %+v", m)
	}
}

func TestApplyBrewAndMise(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	res, err := Apply([]Op{
		{Kind: AddBrew, Name: "jq"},
		{Kind: AddBrew, Name: "iterm2", Cask: true},
		{Kind: PinMise, Name: "node", Version: "22.2.0"},
		{Kind: PinMise, Name: "aqua:foo/bar", Version: "1.0.0"},
	}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range res.Edits {
		got[e.Rel] = string(e.New)
	}
	if len(got) != 2 {
		t.Fatalf("edits = %v", got)
	}
	if want := "brew \"git\"\ncask \"firefox\" # browser\ncask \"iterm2\"\nbrew 'single'\nbrew \"jq\"\n"; got["Brewfile"] != want {
		t.Errorf("Brewfile:\n%s\nwant:\n%s", got["Brewfile"], want)
	}
	want := strings.Replace(baseMise, `"22.1.0" # lts`, `"22.2.0" # lts`, 1)
	want = strings.Replace(want, "go = \"1.27\"\n", "go = \"1.27\"\n\"aqua:foo/bar\" = \"1.0.0\"\n", 1)
	if got["mise/config.toml"] != want {
		t.Errorf("mise config:\n%s\nwant:\n%s", got["mise/config.toml"], want)
	}
}

func TestApplyRejects(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	tests := []struct {
		name string
		op   Op
		want string
	}{
		{"unknown marketplace", Op{Kind: AddPlugin, Name: "x@nowhere"}, "not declared"},
		{"duplicate plugin", Op{Kind: AddPlugin, Name: "first@market"}, "already declared"},
		{"duplicate rule", Op{Kind: AddPermission, Decision: "allow", Command: "git   status"}, "already has a rule"},
		{"missing rule", Op{Kind: RemovePermission, Decision: "deny", Command: "ls"}, "no rule"},
		{"invalid config", Op{Kind: AddPermission, Decision: "deny", Command: "rm -rf *"}, "invalid"},
		{"bad provider", Op{Kind: AddPlugin, Name: "x@market", Providers: []string{"vim"}}, "unknown provider"},
		{"duplicate brew", Op{Kind: AddBrew, Name: "git"}, "already in the Brewfile"},
		{"bad version", Op{Kind: PinMise, Name: "node", Version: "1 2"}, "unsupported"},
		{"args on a url server", Op{Kind: AddMCP, Name: "docs", Server: &Server{URL: "https://x", Args: []string{"a"}}}, "belong to a command"},
		{"unknown kind", Op{Kind: "rm-rf"}, "unknown operation"},
		{"escape in plugin name", Op{Kind: AddPlugin, Name: "\x1b[2Jx@market"}, "plugin id must be"},
		{"control in justification", Op{Kind: AddPermission, Decision: "deny", Command: "dd", Justification: "a\x1b[8mb"}, "printable"},
		{"single quoted duplicate", Op{Kind: AddBrew, Name: "single"}, "already in the Brewfile"},
		{"duplicate marketplace", Op{Kind: AddMarketplace, Name: "market", Source: "o/r"}, "already declared"},
		{"marketplace without source", Op{Kind: AddMarketplace, Name: "x"}, "source is required"},
		{"source on a plugin", Op{Kind: AddPlugin, Name: "a@market", Source: "o/r"}, "only add-marketplace"},
		{"marketplace for a provider that isn't managed", Op{Kind: AddMarketplace, Name: "x", Source: "o/r", Providers: []string{"codex"}}, "invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Apply([]Op{tt.op}, cfg, home)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApplyRefusesFilesOutsideTheRepoAndSymlinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "Brewfile")
	if err := os.WriteFile(outside, []byte("brew \"git\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, home := newRepo(t, strings.Replace(baseConfig, "file: Brewfile", "file: "+outside, 1))
	if _, err := Apply([]Op{{Kind: AddBrew, Name: "jq"}}, cfg, home); err == nil || !strings.Contains(err.Error(), "outside the config repo") {
		t.Errorf("outside: err = %v", err)
	}

	cfg, home = newRepo(t, baseConfig)
	link := filepath.Join(cfg.Root, "Brewfile")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply([]Op{{Kind: AddBrew, Name: "jq"}}, cfg, home); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("symlink: err = %v", err)
	}
}

func TestApplyMiseWithoutToolsTable(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	if err := os.WriteFile(filepath.Join(cfg.Root, "mise/config.toml"), []byte("[settings]\nx = 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply([]Op{{Kind: PinMise, Name: "go", Version: "1.27"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[settings]\nx = 1\n\n[tools]\ngo = \"1.27\"\n"; string(res.Edits[0].New) != want {
		t.Errorf("got %q, want %q", res.Edits[0].New, want)
	}
}

func TestApplyMiseRefusesArrayValue(t *testing.T) {
	cfg, home := newRepo(t, baseConfig)
	if err := os.WriteFile(filepath.Join(cfg.Root, "mise/config.toml"), []byte("[tools]\nnode = [\"20\", \"22\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply([]Op{{Kind: PinMise, Name: "node", Version: "22"}}, cfg, home); err == nil {
		t.Fatal("expected an error")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		op   Op
		tier string
	}{
		{"plugin", Op{Kind: AddPlugin, Name: "a@b"}, TierLow},
		{"deny rule", Op{Kind: AddPermission, Decision: "deny", Command: "rm"}, TierLow},
		{"review rule", Op{Kind: AddPermission, Decision: "review", Command: "rm"}, TierLow},
		{"allow rule", Op{Kind: AddPermission, Decision: "allow", Command: "ls"}, TierHigh},
		{"remove allow", Op{Kind: RemovePermission, Decision: "allow", Command: "ls"}, TierLow},
		{"remove deny", Op{Kind: RemovePermission, Decision: "deny", Command: "rm"}, TierHigh},
		{"remove review", Op{Kind: RemovePermission, Decision: "review", Command: "rm"}, TierHigh},
		{"core formula", Op{Kind: AddBrew, Name: "jq"}, TierLow},
		{"cask", Op{Kind: AddBrew, Name: "iterm2", Cask: true}, TierHigh},
		{"tap formula", Op{Kind: AddBrew, Name: "owner/tap/tool"}, TierHigh},
		{"mise pin", Op{Kind: PinMise, Name: "node", Version: "22"}, TierLow},
		{"mise backend tool", Op{Kind: PinMise, Name: "npm:left-pad", Version: "1.0.0"}, TierHigh},
		{"mcp", Op{Kind: AddMCP, Name: "docs"}, TierHigh},
		{"marketplace", Op{Kind: AddMarketplace, Name: "m", Source: "o/r"}, TierHigh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier, reasons := Classify([]Op{tt.op})
			if tier != tt.tier || (tier == TierHigh) != (len(reasons) > 0) {
				t.Errorf("tier = %s, reasons = %v, want %s", tier, reasons, tt.tier)
			}
		})
	}
	if tier, _ := Classify([]Op{{Kind: AddPlugin, Name: "a@b"}, {Kind: AddMCP, Name: "x"}}); tier != TierHigh {
		t.Error("one high operation makes the proposal high")
	}
}

func TestSensitiveTouches(t *testing.T) {
	s := Sensitive{Home: testutil.Abs("/Users/me"), Extra: []string{testutil.Abs("/work/claude")}}
	tests := []struct {
		text string
		want bool
	}{
		{"~/.claude/settings.json", true},
		{"~/.claude.json", true},
		{"/Users/me/.codex/config.toml", true},
		{"~/.zshrc", true},
		{"~/.ZSHRC", true},
		{"~/.SSH/x", true},
		{"/ETC/hosts", true},
		{"~/.ssh/id_ed25519", true},
		{"/etc/pam.d/sudo_local", true},
		{"/work/claude/settings.json", true},
		{"add ~/.config/opencode/opencode.json", true},
		{"claude plugin foo@market", false},
		{"~/.claudex/file", false},
		{"~/.config/mise/config.toml", false},
		{"/etcetera", false},
		{"brew bundle install", false},
	}
	for _, tt := range tests {
		text := tt.text
		if strings.HasPrefix(strings.ToLower(text), "/etc") {
			if runtime.GOOS == "windows" {
				continue
			}
		} else {
			text = testutil.Abs(text)
		}
		if _, got := s.Touches(text); got != tt.want {
			t.Errorf("Touches(%q) = %v, want %v", text, got, tt.want)
		}
	}
}

func TestAddBrewRefusesFilesAnInsertCouldLandInside(t *testing.T) {
	for _, in := range []string{
		"brew \"foo\", args: [\n  \"HEAD\",\n  \"verbose\"\n]\ncask \"x\"\n",
		"if OS.mac?\n  brew \"a\"\nend\n",
		"brew \"a\"\ncask \"b\" do\nend\n",
	} {
		if _, err := addBrew([]byte(in), "jq", false); err == nil {
			t.Errorf("%q: accepted", in)
		}
	}
	got, err := addBrew([]byte("tap \"x/y\" # [odd] comment\nbrew \"a\", args: [\"HEAD\"] if OS.mac?\n"), "jq", false)
	if err != nil || !strings.HasSuffix(string(got), "brew \"jq\"\n") {
		t.Errorf("one-line entries: %q, %v", got, err)
	}
}

func intp(n int) *int { return &n }

func derefPort(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

const linuxConfig = `agents:
  providers: [claude]
  marketplaces:
    market: owner/repo
  plugins:
    - first@market
`

func newLinuxRepo(t *testing.T) (*config.Config, string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "zakwas.yaml"), []byte(linuxConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFor(filepath.Join(root, "zakwas.yaml"), home, platform.Linux)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, home
}

func TestApplyOnLinux(t *testing.T) {
	cfg, home := newLinuxRepo(t)
	res, err := Apply([]Op{{Kind: AddPlugin, Name: "second@market"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.OS() != platform.Linux || len(res.Config.Agents.Plugins) != 2 {
		t.Errorf("edited config: OS %q, agents %+v", res.Config.OS(), res.Config.Agents)
	}
	tests := []struct {
		name string
		op   Op
		want string
	}{
		{"brew", Op{Kind: AddBrew, Name: "jq"}, "add-brew: brew: not supported on Linux"},
		{"mise", Op{Kind: PinMise, Name: "jq", Version: "1.8.0"}, "pin-mise: mise: not supported on Linux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Apply([]Op{tt.op}, cfg, home); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApplyQuotesEveryYAMLHazardInEveryField(t *testing.T) {
	hazards := []string{"<<", "~", "null", "Null", "true", "False", "yes", "off", "12", "0x1f", "1e3", ".inf", "2001-01-01", "-", "-x", "*a", "&a", "!t", "|", ">", "%x", "@x", "`x", "a: b", "a #b", "x:", "[a]", "{a}"}
	for _, h := range hazards {
		t.Run(h, func(t *testing.T) {
			cfg, home := newRepo(t, baseConfig)
			ops := []Op{
				{Kind: AddMarketplace, Name: "mk", Source: h},
				{Kind: AddMCP, Name: "srv", Server: &Server{Command: h, Args: []string{h, "-y"}}},
			}
			withOAuth := !strings.Contains(h, " ")
			if withOAuth {
				ops = append(ops, Op{Kind: AddMCP, Name: "web", Server: &Server{URL: "https://example.com/mcp", OAuth: &OAuth{ClientID: h, Scope: h}}})
			}
			withRule := !strings.ContainsAny(h, "<>*&|`[]{}")
			command := "git log"
			if withRule {
				command = h
			}
			ops = append(ops, Op{Kind: AddPermission, Decision: "deny", Command: command, Justification: h})
			res, err := Apply(ops, cfg, home)
			if err != nil {
				t.Fatal(err)
			}
			a := res.Config.Agents
			if got := a.Marketplaces["mk"].Source; got != h {
				t.Errorf("marketplace source = %q", got)
			}
			if srv := a.MCP["srv"]; srv.Command != h || len(srv.Args) != 2 || srv.Args[0] != h {
				t.Errorf("command server = %+v", srv)
			}
			if o := a.MCP["web"].OAuth; withOAuth && (o.ClientID != h || o.Scope != h) {
				t.Errorf("oauth = %+v", o)
			}
			found := false
			for _, r := range a.Permissions.Deny {
				found = found || r.Command == strings.Join(strings.Fields(command), " ") && r.Justification == h
			}
			if !found {
				t.Errorf("deny rules = %+v", a.Permissions.Deny)
			}
		})
	}
}

func TestValidateRefusesTextThatCannotBeWrittenBack(t *testing.T) {
	for _, tt := range []struct{ name, source string }{
		{"invalid UTF-8", "a\xffb/c"},
		{"U+FFFE", "a\ufffeb/c"},
		{"U+FFFF", "a\uffffb/c"},
		{"noncharacter U+FDD0", "a\ufdd0b/c"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := Op{Kind: AddMarketplace, Name: "mk", Source: tt.source}.Validate()
			if err == nil || !strings.Contains(err.Error(), "one line of printable text") {
				t.Errorf("err = %v", err)
			}
			err = Op{Kind: AddMCP, Name: "mk", Server: &Server{Command: "x", Args: []string{tt.source}}}.Validate()
			if err == nil || !strings.Contains(err.Error(), "one line of printable text") {
				t.Errorf("mcp arg: err = %v", err)
			}
		})
	}
}
