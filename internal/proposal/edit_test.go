package proposal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
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

const baseBrewfile = "brew \"git\"\ncask \"firefox\" # browser\n"

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
	cfg, err := config.Load(filepath.Join(root, "zakwas.yaml"), home)
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
	if want := "brew \"git\"\nbrew \"jq\"\ncask \"firefox\" # browser\ncask \"iterm2\"\n"; got["Brewfile"] != want {
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
		{"mcp", Op{Kind: AddMCP, Name: "docs"}, TierHigh},
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
	s := Sensitive{Home: "/Users/me", Extra: []string{"/work/claude"}}
	tests := []struct {
		text string
		want bool
	}{
		{"~/.claude/settings.json", true},
		{"~/.claude.json", true},
		{"/Users/me/.codex/config.toml", true},
		{"~/.zshrc", true},
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
		if _, got := s.Touches(tt.text); got != tt.want {
			t.Errorf("Touches(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
