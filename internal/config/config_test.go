package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	p := writeConfig(t, `
protect: {immutable: true}
files:
  - {src: dotfiles/git/config, dst: ~/.config/git/config}
links:
  - {src: dotfiles/zsh/.zshrc, dst: ~/.zshrc}
templates:
  vars: {theme: nord}
  files:
    - {src: a.tmpl, dst: ~/a, mode: 0600}
brew: {file: Brewfile, cleanup: zap, upgrade: true}
mise: {config: dotfiles/mise/config.toml, prune: true}
defaults:
  - {domain: com.apple.dock, key: autohide, value: true}
  - {domain: NSGlobalDomain, key: KeyRepeat, value: 2}
  - {domain: x, key: f, value: 1.5}
  - {domain: x, key: s, value: Dark}
system:
  dirs: [{path: ~/.ssh, mode: 0700}]
  sudoTouchID: true
  sshKey: {path: ~/.ssh/id_ed25519, comment: me}
commands:
  - {name: n, check: "true", run: "true"}
`)
	c, err := Load(p, "/h")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Dir(p)); c.Root != want {
		t.Errorf("Root = %q, want %q", c.Root, want)
	}
	if !c.Mise.Prune {
		t.Error("mise.prune not loaded")
	}
	if !c.Protect.Immutable || len(c.Files) != 1 {
		t.Errorf("protect/files not loaded: %+v %+v", c.Protect, c.Files)
	}
	if got := c.Templates.Files[0].Mode; got != 0o600 {
		t.Errorf("template mode = %o, want 600", got)
	}
	if got := c.System.Dirs[0].Mode; got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
	wantTypes := []any{true, 2, 1.5, "Dark"}
	for i, want := range wantTypes {
		if c.Defaults[i].Value != want {
			t.Errorf("defaults[%d].Value = %#v, want %#v", i, c.Defaults[i].Value, want)
		}
	}
}

func TestLoadResolvesSymlinkedRoot(t *testing.T) {
	real := filepath.Dir(writeConfig(t, linksOnly))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	c, err := Load(filepath.Join(alias, FileName), "/h")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if c.Root != want {
		t.Errorf("Root = %q, want %q", c.Root, want)
	}
}

const linksOnly = "links: [{src: a, dst: ~/a}]\n"

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"unknown field", "linkz: []", []string{"field linkz not found"}},
		{"missing link fields", "links: [{src: a}]", []string{"links[0]: src and dst are required"}},
		{"duplicate destination", `
links: [{src: a, dst: ~/x}]
templates: {files: [{src: b, dst: ~/x}]}`, []string{`templates.files[0]: destination "~/x" overlaps links[0] "~/x"`}},
		{"duplicate across files and links", `
files: [{src: a, dst: ~/x}]
links: [{src: b, dst: ~/x}]`, []string{`links[0]: destination "~/x" overlaps files[0]`}},
		{"trailing slash", `
files: [{src: a, dst: ~/.a}]
links: [{src: b, dst: ~/.a/}]`, []string{"overlaps files[0]"}},
		{"absolute spelling of home", `
files: [{src: a, dst: ~/.a}]
templates: {files: [{src: b, dst: /h/.a}]}`, []string{"overlaps files[0]"}},
		{"case-insensitive overlap", `
files: [{src: a, dst: ~/.Config/x}, {src: b, dst: ~/.config/X}]`, []string{"files[1]: destination"}},
		{"file inside a files dir", `
files: [{src: dir, dst: ~/.cfg}, {src: a, dst: ~/.cfg/x}]`, []string{`files[1]: destination "~/.cfg/x" overlaps files[0]`}},
		{"template inside a files dir", `
files: [{src: dir, dst: ~/.cfg}]
templates: {files: [{src: t, dst: ~/.cfg/sub/x}]}`, []string{"templates.files[0]"}},
		{"link inside a link", `
links: [{src: dir, dst: ~/.cfg}, {src: a, dst: ~/.cfg/x}]`, []string{"links[1]"}},
		{"dir contains earlier file", `
files: [{src: a, dst: ~/.cfg/x}]
links: [{src: dir, dst: ~/.cfg}]`, []string{"links[0]"}},
		{"relative destination", "files: [{src: a, dst: relative/x}]", []string{`files[0]: path "relative/x" must be absolute`}},
		{"env var destination", "links: [{src: a, dst: $HOME/.d}]", []string{"links[0]", "not expanded"}},
		{"other user's home", "templates: {files: [{src: a, dst: ~bob/x}]}", []string{"must be absolute"}},
		{"relative system dir", "system: {dirs: [{path: x}]}", []string{"system.dirs[0]"}},
		{"template mode without leading zero", "templates: {files: [{src: a, dst: ~/a, mode: 644}]}", []string{"mode 644 (octal 1204)"}},
		{"template mode unreadable by owner", "templates: {files: [{src: a, dst: ~/a, mode: 0044}]}", []string{"templates.files[0]: mode"}},
		{"dir mode without leading zero", "system: {dirs: [{path: ~/.ssh, mode: 700}]}", []string{"system.dirs[0]: mode 700 (octal 1274)"}},
		{"dir mode not enterable", "system: {dirs: [{path: ~/.ssh, mode: 0600}]}", []string{"system.dirs[0]: mode"}},
		{"duplicate default", `
defaults:
  - {domain: d, key: k, value: true}
  - {domain: d, key: k, value: true}`, []string{"defaults[1]: d k is already set by defaults[0]"}},
		{"duplicate currentHost default", `
defaults:
  - {domain: d, key: k, value: true, currentHost: true}
  - {domain: d, key: k, value: false, currentHost: true}`, []string{"defaults[1]: d k is already set by defaults[0]"}},
		{"missing file fields", "files: [{dst: ~/x}]", []string{"files[0]: src and dst are required"}},
		{"bad cleanup", "brew: {file: B, cleanup: nuke}", []string{`brew.cleanup: "nuke"`}},
		{"brew without file", "brew: {upgrade: true}", []string{"brew.file is required"}},
		{"unsupported default", "defaults: [{domain: d, key: k, value: [1]}]", []string{"unsupported value"}},
		{"unquoted date default", "defaults: [{domain: d, key: k, value: 2024-01-01}]", []string{"defaults[0] d k: YAML reads 2024-01-01 as a date; quote it"}},
		{"unquoted timestamp default", "defaults: [{domain: d, key: k, value: 2024-01-01T10:00:00Z}]", []string{"YAML reads 2024-01-01T10:00:00Z as a date"}},
		{"template mode 0o unreadable by owner", "templates: {files: [{src: a, dst: ~/a, mode: 0o044}]}", []string{"e.g. 0o644"}},
		{"unknown agent provider", "agents: {providers: [claude, cursor]}", []string{`agents.providers: unknown provider "cursor" (want claude, codex, opencode)`}},
		{"duplicate agent provider", "agents: {providers: [claude, claude]}", []string{`agents.providers: provider "claude" is listed twice`}},
		{"duplicate plugin provider", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, providers: [codex, codex]}]}", []string{`agents.plugins[0].providers: provider "codex" is listed twice`}},
		{"empty agent providers", "agents: {providers: []}", []string{"agents.providers: list at least one provider"}},
		{"plugin from undeclared marketplace", "agents: {plugins: [humanize@sai]}", []string{`agents.plugins[0]: marketplace "sai" of humanize@sai is not declared`}},
		{"plugin id without marketplace", "agents: {marketplaces: {sai: o/sai}, plugins: [humanize]}", []string{`agents.plugins[0]: id "humanize" must be name@marketplace`}},
		{"plugin id with two @", "agents: {marketplaces: {sai: o/sai}, plugins: [a@b@sai]}", []string{"must be name@marketplace"}},
		{"duplicate plugin", "agents: {marketplaces: {sai: o/sai}, plugins: [a@sai, {id: a@sai}]}", []string{"agents.plugins[1]: a@sai is already declared by agents.plugins[0]"}},
		{"marketplace without source", "agents: {marketplaces: {sai: {providers: [claude]}}}", []string{"agents.marketplaces.sai: source is required"}},
		{"null marketplace", "agents: {marketplaces: {sai: }}", []string{"agents.marketplaces.sai: source is required"}},
		{"bad marketplace name", "agents: {marketplaces: {'my market': o/m}}", []string{"agents.marketplaces.my market: name may only contain"}},
		{"marketplace provider not managed", "agents: {providers: [claude], marketplaces: {sai: {source: o/sai, providers: [codex]}}}", []string{`agents.marketplaces.sai.providers: provider "codex" is not in agents.providers`}},
		{"plugin provider not in marketplace", "agents: {marketplaces: {sai: {source: o/sai, providers: [claude]}}, plugins: [{id: a@sai, providers: [opencode]}]}", []string{`agents.plugins[0].providers: provider "opencode" is not in the providers of marketplace sai`}},
		{"unknown marketplace field", "agents: {marketplaces: {sai: {source: o/sai, auto: true}}}", []string{"field auto not found"}},
		{"unknown plugin field", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, scope: user}]}", []string{"field scope not found"}},
		{"unknown agents field", "agents: {marketplace: {}}", []string{"field marketplace not found"}},
		{"relative opencode skills dir", "agents: {opencode: {skillsDir: skills}}", []string{`agents.opencode.skillsDir: "skills" must be absolute or start with ~/`}},
		{"env var opencode skills dir", "agents: {opencode: {skillsDir: $HOME/skills}}", []string{"agents.opencode.skillsDir"}},
		{"marketplace names differing in case", "agents: {marketplaces: {Foo: o/a, foo: o/b}}", []string{"agents.marketplaces.foo: differs from Foo only in case"}},
		{"home-relative instructions", "agents: {instructions: ~/AGENTS.md}", []string{`agents.instructions: "~/AGENTS.md" must be relative to the directory holding zakwas.yaml, or absolute`}},
		{"unknown command hook event", "agents: {hooks: {commands: [{event: install, command: check}]}}", []string{`agents.hooks.commands[0].event: unknown event "install"`}},
		{"empty command hook", "agents: {hooks: {commands: [{event: beforeTool, command: ' '}]}}", []string{"agents.hooks.commands[0].command: command is required"}},
		{"command hook outside providers", "agents: {providers: [claude], hooks: {commands: [{event: stop, command: check, providers: [codex]}]}}", []string{`agents.hooks.commands[0].providers: provider "codex" is not in agents.providers`}},
		{"duplicate command hook", "agents: {providers: [claude], hooks: {commands: [{event: stop, command: check}, {event: stop, command: check}]}}", []string{"agents.hooks.commands[1]: duplicates agents.hooks.commands[0]"}},
		{"unknown opencode field", "agents: {opencode: {dir: ~/x}}", []string{"field dir not found"}},
		{"empty permission", "agents: {permissions: {allow: ['  ']}}", []string{"agents.permissions.allow[0]: command is required"}},
		{"null permission command", "agents: {permissions: {deny: [{providers: [claude]}]}}", []string{"agents.permissions.deny[0]: command is required"}},
		{"permission with a glob", "agents: {permissions: {allow: ['git *']}}", []string{`agents.permissions.allow[0]: "git *" contains '*'`}},
		{"permission with a pipe", "agents: {permissions: {allow: ['ls | sh']}}", []string{`contains '|'`}},
		{"permission with a chain", "agents: {permissions: {allow: ['make && rm']}}", []string{`contains '&'`}},
		{"permission with a variable", "agents: {permissions: {deny: ['rm $HOME']}}", []string{`contains '$'`}},
		{"permission with quotes", `agents: {permissions: {review: ["git commit -m 'x'"]}}`, []string{`contains '\''`}},
		{"permission with a substitution", "agents: {permissions: {allow: ['echo `id`']}}", []string{"contains '`'"}},
		{"permission with a redirect", "agents: {permissions: {allow: ['cat > f']}}", []string{"contains '>'"}},
		{"permission with a backslash", `agents: {permissions: {allow: ['git\ status']}}`, []string{`contains '\\'`}},
		{"permission with a control character", "agents: {permissions: {allow: [\"git\\x07\"]}}", []string{`contains '\a'`}},
		{"permission provider not managed", "agents: {providers: [claude], permissions: {allow: [{command: ls, providers: [codex]}]}}", []string{`agents.permissions.allow[0].providers: provider "codex" is not in agents.providers`}},
		{"empty permission providers", "agents: {permissions: {allow: [{command: ls, providers: []}]}}", []string{"agents.permissions.allow[0].providers: list at least one provider"}},
		{"duplicate permission", "agents: {permissions: {allow: [git status, 'git  status']}}", []string{`agents.permissions.allow[1]: "git status" duplicates agents.permissions.allow[0] for claude`}},
		{"permission in two lists", "agents: {providers: [claude, codex], permissions: {allow: [git push], deny: [{command: git push, providers: [codex]}]}}", []string{`agents.permissions.deny[0]: "git push" is also listed in agents.permissions.allow[0] for codex; give each command one decision per provider`}},
		{"unknown permission field", "agents: {permissions: {allow: [{command: ls, decision: allow}]}}", []string{"field decision not found"}},
		{"unknown permission list", "agents: {permissions: {ask: [ls]}}", []string{"field ask not found"}},
		{"multi-line justification", `agents: {permissions: {deny: [{command: rm, justification: "no\nway"}]}}`, []string{"agents.permissions.deny[0].justification: write it on one line"}},
		{"blank justification", `agents: {providers: [codex], permissions: {deny: [{command: rm, justification: "  "}]}}`, []string{"agents.permissions.deny[0].justification: is blank"}},
		{"reports all errors", `
links: [{src: a}]
commands: [{name: n}]`, []string{"links[0]", "commands[0]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.body), "/h")
			if err == nil {
				t.Fatal("expected error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestLoadAccepts(t *testing.T) {
	tests := []struct{ name, body string }{
		{"sibling prefixes", `
files: [{src: a, dst: ~/.a}, {src: b, dst: ~/.ab}, {src: c, dst: ~/.a-b/x}]
links: [{src: d, dst: /etc/x}]`},
		{"same key in different domains", `
defaults: [{domain: a, key: k, value: 1}, {domain: b, key: k, value: 1}]`},
		{"same key per host and global", `
defaults: [{domain: a, key: k, value: 1}, {domain: a, key: k, value: 1, currentHost: true}]`},
		{"agents shorthands and objects", `
agents:
  providers: [claude, codex]
  upgrade: true
  prune: true
  marketplaces:
    sai: o/sai
    local: {source: ./mp, providers: [claude]}
  plugins:
    - humanize@sai
    - {id: kup@local}
    - {id: x@sai, providers: [codex]}`},
		{"opencode skills dir", `
agents:
  providers: [opencode]
  opencode: {skillsDir: ~/.agents/skills}`},
		{"empty opencode settings", "agents: {providers: [opencode], opencode: {}}"},
		{"instructions", "agents: {instructions: agents/AGENTS.md}"},
		{"klaudiush hooks", "agents: {hooks: {klaudiush: true}}"},
		{"shared command hooks", "agents: {providers: [claude, codex, opencode], hooks: {commands: [{event: beforeTool, command: check}]}}"},
		{"permissions", `
agents:
  providers: [claude, codex]
  permissions:
    allow: [git status, sed -n, {command: peekaboo see, providers: [codex]}, /usr/bin/env -i]
    review: [git push, {command: peekaboo see, providers: [claude]}]
    deny: [{command: git push --force, justification: 'use "git push --force-with-lease"'}, npm run lint:fix]`},
		{"empty permissions", "agents: {permissions: {}}"},
		{"codex decision differs while codex is not named", "agents: {permissions: {allow: [git push], deny: [{command: git push, providers: [codex]}]}}"},
		{"example config", mustRead(t, "../../examples/zakwas.yaml")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(writeConfig(t, tt.body), "/h"); err != nil {
				t.Error(err)
			}
		})
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAgentsProviders(t *testing.T) {
	c, err := Load(writeConfig(t, `
agents:
  marketplaces:
    sai: o/sai
    cl: {source: o/cl, providers: [claude]}
  plugins:
    - a@sai
    - b@cl
    - {id: c@sai, providers: [opencode]}
`), "/h")
	if err != nil {
		t.Fatal(err)
	}
	a := c.Agents
	if got := a.DefaultProviders(); !slices.Equal(got, Providers) {
		t.Errorf("default providers = %v", got)
	}
	if a.Marketplaces["sai"].Source != "o/sai" || a.Plugins[0].ID != "a@sai" {
		t.Errorf("shorthands not decoded: %+v", a)
	}
	for _, tc := range []struct {
		plugin int
		want   []string
	}{
		{0, Providers},
		{1, []string{ProviderClaude}},
		{2, []string{ProviderOpencode}},
	} {
		if got := a.PluginProviders(a.Plugins[tc.plugin]); !slices.Equal(got, tc.want) {
			t.Errorf("plugin %s providers = %v, want %v", a.Plugins[tc.plugin].ID, got, tc.want)
		}
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(deep); err == nil {
		t.Fatal("expected not found")
	}
	want := filepath.Join(root, FileName)
	if err := os.WriteFile(want, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Find(deep)
	if err != nil || got != want {
		t.Fatalf("Find = %q, %v; want %q", got, err, want)
	}
}

func TestPaths(t *testing.T) {
	p := Paths{Home: "/h", Root: "/r"}
	tests := []struct {
		fn   func(string) string
		in   string
		want string
	}{
		{p.Dst, "~", "/h"},
		{p.Dst, "~/.zshrc", "/h/.zshrc"},
		{p.Dst, "/etc/x", "/etc/x"},
		{p.Dst, "~user/x", "~user/x"},
		{p.Src, "dotfiles/a", "/r/dotfiles/a"},
		{p.Src, "/abs", "/abs"},
		{p.Pretty, "/h/.zshrc", "~/.zshrc"},
		{p.Pretty, "/hx/y", "/hx/y"},
		{p.Pretty, "/r/a", "/r/a"},
	}
	for _, tt := range tests {
		if got := tt.fn(tt.in); got != tt.want {
			t.Errorf("(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPermissionsFor(t *testing.T) {
	load := func(t *testing.T, body string) *Agents {
		t.Helper()
		c, err := Load(writeConfig(t, body), "/h")
		if err != nil {
			t.Fatal(err)
		}
		return c.Agents
	}
	render := func(perms []Permission) []string {
		var out []string
		for _, p := range perms {
			s := p.Decision + " " + p.Command()
			if p.Justification != "" {
				s += " (" + p.Justification + ")"
			}
			out = append(out, s)
		}
		return out
	}
	permissions := `
  permissions:
    deny: [git push --force]
    review: [git push]
    allow:
      - git   status
      - {command: peekaboo see, providers: [codex]}
      - {command: sed -n, providers: [claude, opencode]}
`
	tests := []struct {
		name, agents, provider string
		want                   []string
	}{
		{"claude, in decision order", "agents:" + permissions, ProviderClaude, []string{"allow git status", "allow sed -n", "review git push", "deny git push --force"}},
		{"opencode by default", "agents:" + permissions, ProviderOpencode, []string{"allow git status", "allow sed -n", "review git push", "deny git push --force"}},
		{"codex only by name when not named", "agents:" + permissions, ProviderCodex, []string{"allow peekaboo see"}},
		{"codex named", `
agents:
  providers: [claude, codex]
  permissions:
    allow: [git status, {command: peekaboo see, providers: [codex]}, {command: sed -n, providers: [claude]}]
    deny: [{command: git push --force, justification: use --force-with-lease}]`, ProviderCodex, []string{"allow git status", "allow peekaboo see", "deny git push --force (use --force-with-lease)"}},
		{"provider not managed", "agents:\n  providers: [claude]\n  permissions: {allow: [ls]}", ProviderOpencode, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(load(t, tt.agents).PermissionsFor(tt.provider)); !slices.Equal(got, tt.want) {
				t.Errorf("PermissionsFor(%s) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
	var none *Agents
	if got := none.PermissionsFor(ProviderClaude); got != nil {
		t.Errorf("nil agents = %v", got)
	}
}

func TestAgentsGuard(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		enabled   bool
		providers []string
	}{
		{"default", "agents: {}", true, []string{ProviderClaude, ProviderOpencode}},
		{"explicitly on", "agents: {guard: true}", true, []string{ProviderClaude, ProviderOpencode}},
		{"off", "agents: {guard: false}", false, nil},
		{"codex named", "agents: {providers: [claude, codex]}", true, []string{ProviderClaude, ProviderCodex}},
		{"unmanaged providers", "agents: {providers: [opencode]}", true, []string{ProviderOpencode}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.yaml), "/home/u")
			if err != nil {
				t.Fatal(err)
			}
			a := cfg.Agents
			if a.GuardEnabled() != tt.enabled {
				t.Errorf("GuardEnabled = %v, want %v", a.GuardEnabled(), tt.enabled)
			}
			var got []string
			for _, p := range Providers {
				if a.GuardsProvider(p) {
					got = append(got, p)
					perms := a.GuardPermissions(p)
					if len(perms) != len(GuardedCommands) || perms[0].Decision != PermissionDeny || perms[0].Command() != "zakwas apply" {
						t.Errorf("%s guard permissions = %+v", p, perms)
					}
				} else if a.GuardPermissions(p) != nil {
					t.Errorf("%s is not guarded but has guard permissions", p)
				}
			}
			if !slices.Equal(got, tt.providers) {
				t.Errorf("guarded providers = %q, want %q", got, tt.providers)
			}
		})
	}
}

func TestNoAgentsSectionHasNoGuard(t *testing.T) {
	var a *Agents
	if a.GuardEnabled() || a.GuardsProvider(ProviderClaude) || a.GuardPermissions(ProviderClaude) != nil {
		t.Error("a missing agents section is guarded")
	}
}
