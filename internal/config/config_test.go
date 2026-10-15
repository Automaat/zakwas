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
		{"launchd label is required", "launchd: [{program: [/bin/true], schedule: {at: '10:00'}}]", []string{"launchd[0]: label is required"}},
		{"launchd label with a slash", "launchd: [{label: a/b, program: [/bin/true], schedule: {at: '10:00'}}]", []string{`label "a/b" may only hold`}},
		{"launchd duplicate label", `
launchd:
  - {label: a.b, program: [/bin/true], schedule: {at: '10:00'}}
  - {label: A.B, program: [/bin/true], schedule: {at: '11:00'}}`, []string{`launchd[1]: label "A.B" is already used by launchd[0]`}},
		{"launchd plist overlaps a file", `
files: [{src: a, dst: ~/Library/LaunchAgents/a.b.plist}]
launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00'}}]`, []string{"launchd[0]: destination", "overlaps files[0]"}},
		{"launchd program is required", "launchd: [{label: a.b, schedule: {at: '10:00'}}]", []string{"launchd[0]: program is required"}},
		{"launchd relative program", "launchd: [{label: a.b, program: [zakwas], schedule: {at: '10:00'}}]", []string{`program "zakwas" must be an absolute path or start with ~/`}},
		{"launchd program variable", "launchd: [{label: a.b, program: [$HOME/x], schedule: {at: '10:00'}}]", []string{"environment variables are not expanded"}},
		{"launchd no schedule", "launchd: [{label: a.b, program: [/bin/true]}]", []string{"schedule needs 'every' or 'at'"}},
		{"launchd both schedules", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00', every: 1h}}]", []string{"sets both 'every' and 'at'"}},
		{"launchd interval too short", "launchd: [{label: a.b, program: [/bin/true], schedule: {every: 10s}}]", []string{"must be at least 1m0s"}},
		{"launchd interval not a duration", "launchd: [{label: a.b, program: [/bin/true], schedule: {every: soon}}]", []string{`every "soon"`}},
		{"launchd bad time", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '25:00'}}]", []string{`at "25:00" is not HH:MM`}},
		{"launchd weekdays need at", "launchd: [{label: a.b, program: [/bin/true], schedule: {every: 1h, weekdays: [mon]}}]", []string{"weekdays only works with 'at'"}},
		{"launchd unknown weekday", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00', weekdays: [monday]}}]", []string{`"monday" is not one of`}},
		{"launchd repeated weekday", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00', weekdays: [mon, mon]}}]", []string{`"mon" is listed twice`}},
		{"launchd empty weekdays", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00', weekdays: []}}]", []string{"schedule.weekdays is empty"}},
		{"launchd control character in env", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00'}, env: {X: \"bell\\u0007x\"}}]", []string{"launchd[0]: env X", "contains a control character"}},
		{"launchd control character in program", "launchd: [{label: a.b, program: [\"/bin/true\", \"a\\u0001\"], schedule: {at: '10:00'}}]", []string{"program[1]", "contains a control character"}},
		{"launchd control character in log", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00'}, log: \"~/a\\u0000\"}]", []string{"launchd[0]: log"}},
		{"launchd relative log", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00'}, log: out.log}]", []string{"launchd[0]: log:"}},
		{"launchd env name with =", "launchd: [{label: a.b, program: [/bin/true], schedule: {at: '10:00'}, env: {'A=B': x}}]", []string{`env name "A=B"`}},
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
		{"unknown agent provider", "agents: {providers: [claude, cursor]}", []string{`agents.providers: unknown provider "cursor" (want claude, codex, opencode, gemini, copilot)`}},
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
		{"plugins with only gemini", "agents: {providers: [gemini], marketplaces: {sai: o/sai}, plugins: [a@sai]}", []string{"agents.marketplaces and agents.plugins: no provider in agents.providers has plugins"}},
		{"marketplace for gemini", "agents: {providers: [claude, gemini], marketplaces: {sai: {source: o/sai, providers: [gemini]}}}", []string{"agents.marketplaces.sai.providers: gemini has no plugin marketplaces"}},
		{"plugin for gemini", "agents: {providers: [claude, gemini], marketplaces: {sai: o/sai}, plugins: [{id: a@sai, providers: [gemini]}]}", []string{"agents.plugins[0].providers: gemini has no plugins"}},
		{"gemini hook while gemini is not managed", "agents: {providers: [claude], hooks: {commands: [{event: stop, command: check, providers: [gemini]}]}}", []string{`agents.hooks.commands[0].providers: provider "gemini" is not in agents.providers`}},
		{"profile gemini block while gemini is opt-in", "agents: {profiles: {r: {description: d, gemini: {model: m}}}}", []string{"agents.profiles.r: gemini: the profile is not rendered for gemini; Gemini is opt-in"}},
		{"profile gemini override zakwas sets", "agents: {providers: [gemini], profiles: {r: {description: d, gemini: {overrides: {tools: [x]}}}}}", []string{"agents.profiles.r: gemini.overrides.tools: zakwas sets it from the profile's own fields"}},
		{"marketplace provider not managed", "agents: {providers: [claude], marketplaces: {sai: {source: o/sai, providers: [codex]}}}", []string{`agents.marketplaces.sai.providers: provider "codex" is not in agents.providers`}},
		{"plugin provider not in marketplace", "agents: {marketplaces: {sai: {source: o/sai, providers: [claude]}}, plugins: [{id: a@sai, providers: [opencode]}]}", []string{`agents.plugins[0].providers: provider "opencode" is not in the providers of marketplace sai`}},
		{"unknown marketplace field", "agents: {marketplaces: {sai: {source: o/sai, auto: true}}}", []string{"field auto not found"}},
		{"unknown plugin field", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, scope: user}]}", []string{"field scope not found"}},
		{"unknown agents field", "agents: {marketplace: {}}", []string{"field marketplace not found"}},
		{"relative opencode skills dir", "agents: {opencode: {skillsDir: skills}}", []string{`agents.opencode.skillsDir: "skills" must be absolute or start with ~/`}},
		{"env var opencode skills dir", "agents: {opencode: {skillsDir: $HOME/skills}}", []string{"agents.opencode.skillsDir"}},
		{"marketplace names differing in case", "agents: {marketplaces: {Foo: o/a, foo: o/b}}", []string{"agents.marketplaces.foo: differs from Foo only in case"}},
		{"home-relative instructions", "agents: {instructions: ~/AGENTS.md}", []string{`agents.instructions: "~/AGENTS.md" must be relative to the directory holding zakwas.yaml, or absolute`}},
		{"home-relative listed instructions", "agents: {instructions: [a.md, ~/b.md]}", []string{`agents.instructions: "~/b.md" must be relative to the directory holding zakwas.yaml, or absolute`}},
		{"duplicate instructions", "agents: {instructions: [a.md, b.md, a.md]}", []string{`agents.instructions[2]: "a.md" is already listed`}},
		{"blank instructions entry", "agents: {instructions: [a.md, ' ']}", []string{"agents.instructions[1]: file is required"}},
		{"instructions as a mapping", "agents: {instructions: {file: a.md}}", []string{"instructions must be a file or a list of files"}},
		{"shared instructions without instructions", "agents: {sharedInstructions: true}", []string{"agents.sharedInstructions: needs agents.instructions"}},
		{"unknown command hook event", "agents: {hooks: {commands: [{event: install, command: check}]}}", []string{`agents.hooks.commands[0].event: unknown event "install"`}},
		{"empty command hook", "agents: {hooks: {commands: [{event: beforeTool, command: ' '}]}}", []string{"agents.hooks.commands[0].command: command is required"}},
		{"command hook outside providers", "agents: {providers: [claude], hooks: {commands: [{event: stop, command: check, providers: [codex]}]}}", []string{`agents.hooks.commands[0].providers: provider "codex" is not in agents.providers`}},
		{"duplicate command hook", "agents: {providers: [claude], hooks: {commands: [{event: stop, command: check}, {event: stop, command: check}]}}", []string{"agents.hooks.commands[1]: duplicates agents.hooks.commands[0]"}},
		{"unknown opencode field", "agents: {opencode: {dir: ~/x}}", []string{"field dir not found"}},
		{"mcp server without command or url", "agents: {mcp: {docs: {}}}", []string{"agents.mcp.docs: set command for a stdio server or url for an HTTP server"}},
		{"mcp server with command and url", "agents: {mcp: {docs: {command: x, url: 'https://e.com'}}}", []string{"agents.mcp.docs: set either command or url, not both"}},
		{"mcp name with a space", "agents: {mcp: {'my server': {command: x}}}", []string{"agents.mcp.my server: name may only contain"}},
		{"mcp names differing in case", "agents: {mcp: {Foo: {command: x}, foo: {command: x}}}", []string{"agents.mcp.foo: differs from Foo only in case"}},
		{"mcp stdio with headers", "agents: {mcp: {a: {command: x, headers: {A: b}}}}", []string{"agents.mcp.a: headers only applies to a server with a url"}},
		{"mcp stdio with bearer token", "agents: {mcp: {a: {command: x, bearerTokenEnv: T}}}", []string{"agents.mcp.a: bearerTokenEnv only applies to a server with a url"}},
		{"mcp http with args", "agents: {mcp: {a: {url: 'https://e.com', args: [x]}}}", []string{"agents.mcp.a: args only applies to a server with a command"}},
		{"mcp http with env vars", "agents: {mcp: {a: {url: 'https://e.com', envVars: [X]}}}", []string{"agents.mcp.a: envVars only applies to a server with a command"}},
		{"mcp url scheme", "agents: {mcp: {a: {url: 'ftp://e.com'}}}", []string{`agents.mcp.a: url "ftp://e.com" must be an http:// or https:// URL`}},
		{"mcp url without host", "agents: {mcp: {a: {url: 'https://'}}}", []string{"must be an http:// or https:// URL"}},
		{"mcp url with a password", "agents: {mcp: {a: {url: 'https://u:pw@e.com/mcp'}}}", []string{"agents.mcp.a: url holds a user name or password"}},
		{"mcp env name", "agents: {mcp: {a: {command: x, env: {'A B': c}}}}", []string{`agents.mcp.a: env name "A B" is not a valid environment variable name`}},
		{"mcp env var name", "agents: {mcp: {a: {command: x, envVars: ['1X']}}}", []string{`agents.mcp.a: envVars[0] "1X" is not a valid environment variable name`}},
		{"mcp duplicate env var", "agents: {mcp: {a: {command: x, envVars: [X, X]}}}", []string{`agents.mcp.a: envVars lists "X" twice`}},
		{"mcp env var also in env", "agents: {mcp: {a: {command: x, env: {X: '1'}, envVars: [X]}}}", []string{`agents.mcp.a: "X" is in both env and envVars`}},
		{"mcp bearer token env name", "agents: {mcp: {a: {url: 'https://e.com', bearerTokenEnv: 'a-b'}}}", []string{`agents.mcp.a: bearerTokenEnv "a-b" is not a valid environment variable name`}},
		{"mcp header env name", "agents: {mcp: {a: {url: 'https://e.com', headerEnv: {X-Key: 'a b'}}}}", []string{`agents.mcp.a: headerEnv "X-Key": "a b" is not a valid environment variable name`}},
		{"mcp header name", "agents: {mcp: {a: {url: 'https://e.com', headers: {'X Key': v}}}}", []string{`agents.mcp.a: headers name "X Key" is not a valid HTTP header name`}},
		{"mcp header set twice", "agents: {mcp: {a: {url: 'https://e.com', headers: {x-key: v}, headerEnv: {X-Key: K}}}}", []string{`agents.mcp.a: header "X-Key" is set in both headers and headerEnv`}},
		{"mcp bearer token and authorization header", "agents: {mcp: {a: {url: 'https://e.com', headers: {authorization: v}, bearerTokenEnv: T}}}", []string{`agents.mcp.a: header "Authorization" is set in both headers and bearerTokenEnv`}},
		{"profile naming an undeclared mcp server", "agents: {profiles: {r: {description: d, mcp: {docs: allow}}}}", []string{"agents.profiles.r: mcp.docs: server is not declared in agents.mcp"}},
		{"profile mcp decision", "agents: {mcp: {docs: {command: x}}, profiles: {r: {description: d, mcp: {docs: maybe}}}}", []string{`agents.profiles.r: mcp.docs "maybe" is not one of allow, ask, deny`}},
		{"profile without description", "agents: {profiles: {r: {edit: deny}}}", []string{"agents.profiles.r: description is required"}},
		{"profile name", "agents: {profiles: {Review: {description: d}}}", []string{"agents.profiles.Review: name may only contain lowercase letters"}},
		{"profile named null", "agents: {profiles: {null: {description: d}}}", []string{"agents.profiles: a profile cannot be named null"}},
		{"profile named with a tilde", "agents: {profiles: {~: {description: d}, r: {description: d}}}", []string{"agents.profiles: a profile cannot be named null"}},
		{"profile map reached through an alias", "agents: {mcp: &m {null: {command: x}}, profiles: *m}", []string{"agents.profiles: a profile cannot be named null"}},
		{"profile map reached through a merge key", "agents: {profiles: {<<: {null: {description: d}}}}", []string{"agents.profiles: a profile cannot be named null"}},
		{"profile map merged from a list of aliases", "agents: {mcp: &a {null: {command: x}}, profiles: {<<: [*a]}}", []string{"agents.profiles: a profile cannot be named null"}},
		{"agents reached through a merge key", "agents:\n  <<: {profiles: {null: {description: d}}}", []string{"line 2:", "agents.profiles: a profile cannot be named null"}},
		{"profiles reached through a merge key in agents", "agents:\n  providers: [claude]\n  <<: {profiles: {ok: {description: d}, null: {description: d}}}", []string{"line 3:", "a profile cannot be named null"}},
		{"agents reached through a root merge key", "<<: {agents: {profiles: {null: {description: d}}}}", []string{"line 1:", "a profile cannot be named null"}},
		{"agents nested two merge keys deep", "agents: {<<: [{<<: {profiles: {null: {description: d}}}}]}", []string{"line 1:", "a profile cannot be named null"}},
		{"profile named by an alias to null", "agents:\n  mcp: {&k null: {command: x}}\n  profiles:\n    ok: {description: d}\n    *k : {description: d}", []string{"line 5:", "a profile cannot be named null"}},
		{"profile named by an alias to a tilde", "agents:\n  mcp: {&k ~: {command: x}}\n  profiles:\n    *k : {description: d}", []string{"line 4:", "a profile cannot be named null"}},
		{"profile named by an alias in a merged map", "agents:\n  mcp: {&k null: {command: x}}\n  profiles:\n    <<: {*k : {description: d}}", []string{"line 4:", "a profile cannot be named null"}},
		{"profile named by an alias reached through a merge key", "agents:\n  mcp: {&k null: {command: x}}\n  <<: {profiles: {*k : {description: d}}}", []string{"line 3:", "a profile cannot be named null"}},
		{"null profile name below 17 merge keys", "agents: {profiles: " + strings.Repeat("{<<: ", 17) + "{null: {description: d}}" + strings.Repeat("}", 17) + "}", []string{"line 1:", "a profile cannot be named null"}},
		{"null profile name below 100 merge keys in agents", "agents: " + strings.Repeat("{<<: ", 100) + "{profiles: {null: {description: d}}}" + strings.Repeat("}", 100), []string{"line 1:", "a profile cannot be named null"}},
		{"null profile name below 40 merge sequences", "agents: {profiles: " + strings.Repeat("{<<: [", 40) + "{null: {description: d}}" + strings.Repeat("]}", 40) + "}", []string{"line 1:", "a profile cannot be named null"}},
		{"profile named by an alias to a merge key", "agents:\n  mcp: {&m <<: {a: {command: x}}}\n  profiles:\n    *m : {description: d}", []string{"agents.profiles.<<: name may only contain"}},
		{"profile named by an empty key", "agents:\n  profiles:\n    ? \n    : {description: d}", []string{"line 3:", "a profile cannot be named null"}},
		{"profile name too long", "agents: {profiles: {" + strings.Repeat("a", MaxProfileName+1) + ": {description: d}}}", []string{"name is 65 characters long; at most 64 are allowed"}},
		{"profile tool", "agents: {profiles: {r: {description: d, tools: [read, fly]}}}", []string{`agents.profiles.r: tools[1]: unknown tool "fly"`}},
		{"profile edit without edit tool", "agents: {profiles: {r: {description: d, tools: [read], edit: allow}}}", []string{"agents.profiles.r: edit is allow but tools leaves out edit"}},
		{"profile shell decision", "agents: {profiles: {r: {description: d, shell: sometimes}}}", []string{`agents.profiles.r: shell "sometimes" is not one of allow, ask, deny`}},
		{"profile sandbox", "agents: {profiles: {r: {description: d, sandbox: open}}}", []string{`agents.profiles.r: sandbox "open" is not one of`}},
		{"profile reviewer", "agents: {profiles: {r: {description: d, reviewer: bot}}}", []string{`agents.profiles.r: reviewer "bot" is not one of user, auto`}},
		{"profile provider not managed", "agents: {providers: [claude], profiles: {r: {description: d, providers: [codex]}}}", []string{`agents.profiles.r.providers: provider "codex" is not in agents.providers`}},
		{"profile block for a provider it skips", "agents: {profiles: {r: {description: d, providers: [claude], codex: {model: m}}}}", []string{"agents.profiles.r: codex: the profile is not rendered for codex"}},
		{"profile override of a managed key", "agents: {profiles: {r: {description: d, claude: {overrides: {tools: x}}}}}", []string{"agents.profiles.r: claude.overrides.tools: zakwas sets it from the profile's own fields"}},
		{"profile codex block while codex is opt-in", "agents: {profiles: {r: {description: d, codex: {model: m}}}}", []string{"agents.profiles.r: codex: the profile is not rendered for codex; Codex is opt-in"}},
		{"empty profile provider block", "agents: {profiles: {r: {description: d, opencode: {}}}}", []string{"agents.profiles.r: opencode: set a model or overrides, or remove it"}},
		{"skill without path", "agents: {skills: [{git: o/r}]}", []string{"agents.skills[0]: path is required"}},
		{"skill path is the repo root", "agents: {skills: [.]}", []string{`agents.skills[0]: path "." must point at a skill directory`}},
		{"skill path outside the repo", "agents: {skills: [{git: o/r, path: ../x}]}", []string{`agents.skills[0]: path "../x" must be relative to the root of the git repository`}},
		{"skill absolute git path", "agents: {skills: [{git: o/r, path: /x}]}", []string{"must be relative to the root of the git repository"}},
		{"skill path in .git", "agents: {skills: [{git: o/r, path: .git/x}]}", []string{`path ".git/x" goes into .git`}},
		{"skill ref without git", "agents: {skills: [{path: s/a, ref: v1}]}", []string{"agents.skills[0]: ref only applies to a skill with git"}},
		{"skill git with ref", "agents: {skills: [{git: 'o/r#v1', path: s/a}]}", []string{"set ref instead"}},
		{"skill git option", "agents: {skills: [{git: --upload-pack=x, path: s/a}]}", []string{"without a leading '-'"}},
		{"skill ref option", "agents: {skills: [{git: o/r, path: s/a, ref: -x}]}", []string{`ref "-x" must not start with '-'`}},
		{"skill git with a space", "agents: {skills: [{git: 'o/r x', path: s/a}]}", []string{"git \"o/r x\" must not contain spaces"}},
		{"skill name", "agents: {skills: [s/.hidden]}", []string{`name ".hidden" (the last element of path)`}},
		{"duplicate skill name", "agents: {skills: [a/foo, {git: o/r, path: b/Foo}]}", []string{`agents.skills[1]: skill "Foo" is already declared by agents.skills[0]`}},
		{"skill provider not managed", "agents: {providers: [claude], skills: [{path: s/a, providers: [codex]}]}", []string{`agents.skills[0].providers: provider "codex" is not in agents.providers`}},
		{"skill unknown field", "agents: {skills: [{path: s/a, name: b}]}", []string{"field name not found"}},
		{"mcp oauth and bearer token", "agents: {mcp: {a: {url: 'https://e.com', oauth: true, bearerTokenEnv: T}}}", []string{"agents.mcp.a: oauth can't be combined with an Authorization header (bearerTokenEnv)"}},
		{"mcp control character", "agents: {mcp: {a: {command: x, args: [\"a\\nb\"]}}}", []string{"agents.mcp.a: values may not hold control characters"}},
		{"mcp provider not managed", "agents: {providers: [claude], mcp: {a: {command: x, providers: [codex]}}}", []string{`agents.mcp.a.providers: provider "codex" is not in agents.providers`}},
		{"mcp unknown field", "agents: {mcp: {a: {command: x, cmd: y}}}", []string{"field cmd not found"}},
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
		{"profile named with a quoted null", `agents: {profiles: {"null": {description: d}}}`},
		{"profile reached through a merge key", "agents: {<<: {profiles: {ok: {description: d}}}}"},
		{"profile map overriding a null name in a merged one", "agents:\n  profiles: {ok: {description: d}}\n  <<: {profiles: {null: {description: d}}}"},
		{"mcp server named null", "agents: {mcp: {null: {command: x}}}"},
		{"mcp reached through a merge key with a null name", "agents: {<<: {mcp: {~: {command: x}}}}"},
		{"profile named by an alias to a string", "agents: {mcp: {&k ok: {command: x}}, profiles: {*k : {description: d}}}"},
		{"mcp server named by an alias to null", "agents: {mcp: {&k null: {command: x}, b: {command: x}}, providers: [claude]}"},
		{"profile name at the length limit", "agents: {profiles: {" + strings.Repeat("a", MaxProfileName) + ": {description: d}}}"},
		{"launchd jobs", `
launchd:
  - label: com.example.daily
    program: [~/.local/bin/check, --quiet]
    schedule: {at: '10:00', weekdays: [mon, tue, wed, thu, fri]}
    env: {PATH: /usr/bin}
    log: ~/Library/Logs/daily.log
  - label: com.example.poll
    program: [/bin/sh, -c, echo hi]
    schedule: {every: 30m}
    runAtLoad: true`},
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
		{"several instructions", "agents: {instructions: [agents/base.md, agents/claude.md], sharedInstructions: true}"},
		{"empty instructions", "agents: {instructions: [], sharedInstructions: false}"},
		{"klaudiush hooks", "agents: {hooks: {klaudiush: true}}"},
		{"shared command hooks", "agents: {providers: [claude, codex, opencode], hooks: {commands: [{event: beforeTool, command: check}]}}"},
		{"permissions", `
agents:
  providers: [claude, codex]
  permissions:
    allow: [git status, sed -n, {command: peekaboo see, providers: [codex]}, /usr/bin/env -i]
    review: [git push, {command: peekaboo see, providers: [claude]}]
    deny: [{command: git push --force, justification: 'use "git push --force-with-lease"'}, npm run lint:fix]`},
		{"mcp servers", `
agents:
  providers: [claude, codex, opencode]
  mcp:
    docs: {command: npx, args: [-y, docs-mcp], env: {LANG: C}, envVars: [DOCS_TOKEN]}
    web: {url: 'https://e.com/mcp', bearerTokenEnv: WEB_TOKEN, headers: {X-Org: acme}, headerEnv: {X-Key: KEY}, providers: [claude, codex]}
    linear: {url: 'https://mcp.linear.app/mcp', oauth: true}`},
		{"empty mcp", "agents: {mcp: {}}"},
		{"skills", `
agents:
  providers: [claude, codex]
  skills:
    - skills/mine
    - ~/work/skills/other
    - {git: o/skills, path: skills/pdf, ref: v1}
    - {path: skills/c, providers: [codex]}`},
		{"empty skills", "agents: {skills: []}"},
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
		{0, []string{ProviderClaude, ProviderCodex, ProviderOpencode, ProviderCopilot}},
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

func TestGeminiIsOptIn(t *testing.T) {
	load := func(yaml string) *Agents {
		t.Helper()
		c, err := Load(writeConfig(t, yaml), "/h")
		if err != nil {
			t.Fatal(err)
		}
		return c.Agents
	}
	targets := func(a *Agents) map[string][]string {
		out := map[string][]string{}
		for _, p := range []string{ProviderClaude, ProviderGemini} {
			for _, s := range a.MCPFor(p) {
				out[p] = append(out[p], s.Name)
			}
		}
		return out
	}
	unnamed := load("agents: {mcp: {a: {command: a}, b: {command: b, providers: [gemini]}}}")
	if unnamed.Names(ProviderGemini) || !unnamed.Wants(ProviderGemini) {
		t.Errorf("an entry naming gemini: names %v, wants %v; want false, true", unnamed.Names(ProviderGemini), unnamed.Wants(ProviderGemini))
	}
	if got := targets(unnamed); !slices.Equal(got[ProviderGemini], []string{"b"}) || !slices.Equal(got[ProviderClaude], []string{"a"}) {
		t.Errorf("unnamed targets = %v", got)
	}
	if untouched := load("agents: {mcp: {a: {command: a}}}"); untouched.Wants(ProviderGemini) || len(untouched.MCPFor(ProviderGemini)) != 0 {
		t.Error("a config that does not name gemini wants it")
	}
	named := load("agents: {providers: [claude, gemini], mcp: {a: {command: a}, b: {command: b, providers: [claude]}}}")
	if !named.Wants(ProviderGemini) || !slices.Equal(targets(named)[ProviderGemini], []string{"a"}) {
		t.Errorf("named targets = %v", targets(named))
	}
	if load("agents: {providers: [claude]}").Wants(ProviderGemini) {
		t.Error("a config that leaves gemini out wants it")
	}
	for _, provider := range []string{ProviderCodex, ProviderGemini} {
		if !OptIn(provider) {
			t.Errorf("%s is not opt-in", provider)
		}
	}
	if OptIn(ProviderClaude) || OptIn(ProviderOpencode) {
		t.Error("claude and opencode are not opt-in")
	}
	plugins := load("agents: {providers: [claude, gemini], marketplaces: {sai: o/sai}, plugins: [a@sai]}")
	if got := plugins.PluginProviders(plugins.Plugins[0]); slices.Contains(got, ProviderGemini) {
		t.Errorf("plugin providers = %v, gemini has no plugins", got)
	}
}

func TestAgentsMCPFor(t *testing.T) {
	c, err := Load(writeConfig(t, `
agents:
  mcp:
    b: {command: b}
    a: {command: a, providers: [codex, opencode]}
    c: {url: 'https://e.com', providers: [claude]}
`), "/h")
	if err != nil {
		t.Fatal(err)
	}
	names := func(provider string) []string {
		var out []string
		for _, s := range c.Agents.MCPFor(provider) {
			out = append(out, s.Name)
		}
		return out
	}
	for provider, want := range map[string][]string{
		ProviderClaude:   {"b", "c"},
		ProviderCodex:    {"a"},
		ProviderOpencode: {"a", "b"},
	} {
		if got := names(provider); !slices.Equal(got, want) {
			t.Errorf("%s servers = %v, want %v", provider, got, want)
		}
	}
	if got := (*Agents)(nil).MCPFor(ProviderClaude); got != nil {
		t.Errorf("nil agents servers = %v", got)
	}
}

func TestAgentsSkillsFor(t *testing.T) {
	c, err := Load(writeConfig(t, `
agents:
  skills:
    - skills/b
    - {path: skills/a, providers: [codex, opencode]}
    - {git: o/r, path: x/c, providers: [claude]}
`), "/h")
	if err != nil {
		t.Fatal(err)
	}
	names := func(provider string) []string {
		var out []string
		for _, s := range c.Agents.SkillsFor(provider) {
			out = append(out, s.Name)
		}
		return out
	}
	for provider, want := range map[string][]string{
		ProviderClaude:   {"b", "c"},
		ProviderCodex:    {"a"},
		ProviderOpencode: {"a", "b"},
	} {
		if got := names(provider); !slices.Equal(got, want) {
			t.Errorf("%s skills = %v, want %v", provider, got, want)
		}
	}
	for provider, want := range map[string]bool{ProviderClaude: true, ProviderCodex: true, ProviderOpencode: true} {
		if got := c.Agents.NamesInSkill(provider); got != want {
			t.Errorf("NamesInSkill(%s) = %v, want %v", provider, got, want)
		}
	}
	if got := (*Agents)(nil).SkillsFor(ProviderClaude); got != nil {
		t.Errorf("nil agents skills = %v", got)
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

func TestMCPPromptTools(t *testing.T) {
	tests := []struct {
		name string
		s    MCPServer
		want []string
	}{
		{"zakwas with apply", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply"}}, []string{MCPApplyTool}},
		{"single dash and path", MCPServer{Command: "/opt/homebrew/bin/zakwas", Args: []string{"mcp", "-c", "/x/zakwas.yaml", "-enable-apply"}}, []string{MCPApplyTool}},
		{"explicit true", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply=true"}}, []string{MCPApplyTool}},
		{"read only", MCPServer{Command: "zakwas", Args: []string{"mcp"}}, nil},
		{"numeric true", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply=1"}}, []string{MCPApplyTool}},
		{"capitalized", MCPServer{Command: "zakwas", Args: []string{"mcp", "-enable-apply=TRUE"}}, []string{MCPApplyTool}},
		{"short true", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply=t"}}, []string{MCPApplyTool}},
		{"numeric false", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply=0"}}, nil},
		{"not a flag", MCPServer{Command: "zakwas", Args: []string{"mcp", "enable-apply"}}, nil},
		{"explicit false", MCPServer{Command: "zakwas", Args: []string{"mcp", "--enable-apply=false"}}, nil},
		{"another command", MCPServer{Command: "zakwas", Args: []string{"plan", "--enable-apply"}}, nil},
		{"another binary", MCPServer{Command: "npx", Args: []string{"mcp", "--enable-apply"}}, nil},
		{"no args", MCPServer{Command: "zakwas"}, nil},
		{"http", MCPServer{URL: "https://example.com/mcp"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.PromptTools(); !slices.Equal(got, tc.want) {
				t.Errorf("PromptTools() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPathsOverlapFollowsSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	other := filepath.Join(root, "other")
	for _, d := range []string{filepath.Join(real, "sub"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias")
	chain := filepath.Join(root, "chain")
	leaf := filepath.Join(real, "leaf")
	leafChain := filepath.Join(root, "leaf-chain")
	file := filepath.Join(other, "file")
	fileLink := filepath.Join(root, "file-link")
	loopA := filepath.Join(root, "loop-a")
	loopB := filepath.Join(root, "loop-b")
	dangling := filepath.Join(root, "dangling")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, l := range [][2]string{{real, alias}, {alias, chain}, {other, leaf}, {leaf, leafChain}, {file, fileLink}, {loopB, loopA}, {loopA, loopB}, {filepath.Join(root, "gone"), dangling}} {
		if err := os.Symlink(l[0], l[1]); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"same spelling", filepath.Join(real, "sub", "f"), filepath.Join(real, "sub", "f"), true},
		{"file through a symlinked dir", filepath.Join(real, "sub", "f"), filepath.Join(alias, "sub", "f"), true},
		{"file through a chain of symlinks", filepath.Join(real, "sub", "f"), filepath.Join(chain, "sub", "f"), true},
		{"directory and a file inside it through a symlink", filepath.Join(real, "sub"), filepath.Join(alias, "sub", "f"), true},
		{"missing tail below a symlinked dir", filepath.Join(real, "sub", "new", "f"), filepath.Join(alias, "sub", "new", "f"), true},
		{"differing case through a symlinked dir", filepath.Join(real, "sub", "F"), filepath.Join(alias, "SUB", "f"), true},
		{"siblings through a symlinked dir", filepath.Join(real, "sub", "a"), filepath.Join(alias, "sub", "b"), false},
		{"different directories", filepath.Join(real, "sub", "f"), filepath.Join(other, "f"), false},
		{"last element symlinked to a directory", leaf, filepath.Join(other, "x"), true},
		{"last element symlinked to a directory, same spelling", leaf, leaf, true},
		{"file inside a last element symlinked to a directory", filepath.Join(leaf, "x"), filepath.Join(other, "x"), true},
		{"last element symlinked to a directory through a chain", leafChain, filepath.Join(other, "x"), true},
		{"last element symlinked to a directory, other sibling", leaf, filepath.Join(real, "sub", "x"), false},
		{"last element symlinked to a file stays as written", fileLink, file, false},
		{"last element symlinked to a file, same spelling", fileLink, fileLink, true},
		{"last element in a symlink loop stays as written", loopA, filepath.Join(root, "x"), false},
		{"last element in a symlink loop, same spelling", loopA, loopA, true},
		{"dangling last element stays as written", dangling, filepath.Join(root, "gone"), false},
		{"missing paths stay lexical", filepath.Join(root, "gone", "f"), filepath.Join(root, "gone", "f"), true},
		{"missing sibling paths", filepath.Join(root, "gone", "a"), filepath.Join(root, "gone", "b"), false},
		{"unrelated missing roots", "/zakwas-missing/a/f", "/zakwas-missing/b/f", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PathsOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("PathsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got := PathsOverlap(tt.b, tt.a); got != tt.want {
				t.Errorf("PathsOverlap(%q, %q) = %v, want %v", tt.b, tt.a, got, tt.want)
			}
		})
	}
}
