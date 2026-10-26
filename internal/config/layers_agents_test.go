package config

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/testutil"
)

var (
	absExtra  = testutil.Abs("/abs/extra.md")
	absShared = testutil.Abs("/abs/shared.md")
	absOne    = testutil.Abs("/abs/one.md")
	absTwo    = testutil.Abs("/abs/two.md")
)

const agentsRoot = `
agents:
  guard: false
  providers: [claude, opencode]
  prune: true
  marketplaces:
    sai: owner/sai
    local: ./market
  plugins:
    - humanize@sai
  instructions: root.md
  hooks:
    commands:
      - {event: stop, command: /bin/true}
  permissions:
    allow:
      - command: git status
    deny:
      - command: rm -rf
  mcp:
    root-srv: {command: /bin/echo}
  profiles:
    reviewer: {description: Reviews, prompt: reviewer.md, mcp: {root-srv: allow}}
  skills:
    - path: skills/root-skill
`

var agentsLayer = `
agents:
  marketplaces:
    work: owner/work
    work-local: ./market
    work-home: ~/market
  plugins:
    - tool@work
    - extra@sai
  instructions: [work.md, '` + absExtra + `']
  hooks:
    klaudiush: true
    commands:
      - {event: afterTool, command: /bin/true}
  permissions:
    allow:
      - command: make test
    review:
      - command: git push
  mcp:
    work-srv: {url: "https://mcp.example.com/mcp"}
  profiles:
    auditor: {description: Audits, prompt: prompts/auditor.md, mcp: {work-srv: ask, root-srv: deny}}
  skills:
    - path: skills/work-skill
    - path: skills/remote
      git: owner/skills
    - path: ~/skills/home-skill
`

func parseAgentsLayers(t *testing.T, rootBody string, layerBodies ...string) (*Config, string, []Layer, error) {
	t.Helper()
	root := writeConfig(t, rootBody)
	var layers []Layer
	for i, body := range layerBodies {
		layers = append(layers, writeLayer(t, "l"+string(rune('0'+i)), body))
	}
	c, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, layers)
	return c, root, layers, err
}

func TestParseLayeredMergesAgents(t *testing.T) {
	c, _, layers, err := parseAgentsLayers(t, agentsRoot, agentsLayer)
	if err != nil {
		t.Fatal(err)
	}
	a := c.Agents
	dir := filepath.Dir(layers[0].Path)

	if got, want := slices.Sorted(maps.Keys(a.Marketplaces)), []string{"local", "sai", "work", "work-home", "work-local"}; !slices.Equal(got, want) {
		t.Errorf("marketplaces = %v, want %v", got, want)
	}
	if got := a.Marketplaces["local"].Source; got != "./market" {
		t.Errorf("the main config's local marketplace = %q, want it untouched", got)
	}
	if got, want := a.Marketplaces["work-local"].Source, filepath.Join(dir, "market"); got != want {
		t.Errorf("a layer's local marketplace = %q, want it under the layer's directory: %q", got, want)
	}
	if got := a.Marketplaces["work-home"].Source; got != "~/market" {
		t.Errorf("a home marketplace = %q, want it untouched", got)
	}
	if got := a.Marketplaces["work"].Source; got != "owner/work" {
		t.Errorf("a github marketplace = %q, want it untouched", got)
	}

	var plugins []string
	for _, p := range a.Plugins {
		plugins = append(plugins, p.ID)
	}
	if got, want := strings.Join(plugins, " "), "humanize@sai tool@work extra@sai"; got != want {
		t.Errorf("plugins = %q, want the main config's first: %q", got, want)
	}

	if got, want := strings.Join(a.Instructions, " "), strings.Join([]string{"root.md", filepath.Join(dir, "work.md"), absExtra}, " "); got != want {
		t.Errorf("instructions = %q, want root first, each layer file under its own directory: %q", got, want)
	}

	if len(a.Permissions.Allow) != 2 || a.Permissions.Allow[0].Command != "git status" || a.Permissions.Allow[1].Command != "make test" {
		t.Errorf("allow = %+v", a.Permissions.Allow)
	}
	if len(a.Permissions.Review) != 1 || len(a.Permissions.Deny) != 1 {
		t.Errorf("review %+v, deny %+v", a.Permissions.Review, a.Permissions.Deny)
	}
	if len(a.Hooks.Commands) != 2 || !a.Hooks.Klaudiush {
		t.Errorf("hooks = %+v", a.Hooks)
	}
	if len(a.MCP) != 2 || a.MCP["work-srv"].URL == "" || a.MCP["root-srv"].Command == "" {
		t.Errorf("mcp = %+v", a.MCP)
	}
	if got := a.Profiles["auditor"].Prompt; got != filepath.Join(dir, "prompts", "auditor.md") {
		t.Errorf("a layer's prompt = %q", got)
	}
	if got := a.Profiles["reviewer"].Prompt; got != "reviewer.md" {
		t.Errorf("the main config's prompt = %q, want it untouched", got)
	}

	var skills []string
	for _, s := range a.Skills {
		skills = append(skills, s.Path)
	}
	want := []string{"skills/root-skill", filepath.Join(dir, "skills", "work-skill"), "skills/remote", "~/skills/home-skill"}
	if !slices.Equal(skills, want) {
		t.Errorf("skills = %q, want %q", skills, want)
	}

	if a.GuardEnabled() || !a.Prune || !slices.Equal(a.Providers, []string{"claude", "opencode"}) {
		t.Errorf("root-only settings changed: guard %v prune %v providers %v", a.Guard, a.Prune, a.Providers)
	}
}

func TestParseLayeredAgentsDoesNotChangeTheMainConfig(t *testing.T) {
	root := writeConfig(t, agentsRoot)
	layer := writeLayer(t, "work", agentsLayer)
	data := mustReadBytes(t, root)
	main, err := ParseFor(data, root, t.TempDir(), platform.Darwin)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ParseLayered(data, root, t.TempDir(), platform.Darwin, []Layer{layer})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Agents == main.Agents {
		t.Fatal("the merged config shares the main config's agents section")
	}
	again, err := ParseFor(data, root, t.TempDir(), platform.Darwin)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Agents.Plugins) != 1 || len(again.Agents.MCP) != 1 || len(again.Agents.Marketplaces) != 2 {
		t.Errorf("a fresh parse of the main config changed: %+v", again.Agents)
	}
}

func TestParseLayeredAgentsOnlyInLayersWithoutLayerAgents(t *testing.T) {
	c, _, _, err := parseAgentsLayers(t, agentsRoot, "files: [{src: x, dst: ~/.x}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Agents.Plugins) != 1 || len(c.Agents.Instructions) != 1 {
		t.Errorf("agents = %+v", c.Agents)
	}
	c, _, _, err = parseAgentsLayers(t, "files: [{src: a, dst: ~/.a}]\n", "agents:\nfiles: [{src: x, dst: ~/.x}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Agents != nil {
		t.Errorf("agents = %+v, want none", c.Agents)
	}
}

func TestParseLayeredAgentsRejects(t *testing.T) {
	tests := []struct {
		name   string
		layers []string
		want   []string
	}{
		{"providers is root-only", []string{"agents: {providers: [claude]}\n"}, []string{`layer "l0" is invalid`, "zakwas layer remove l0", "agents.providers is set in a layer"}},
		{"prune is root-only", []string{"agents: {prune: false}\n"}, []string{"agents.prune is set in a layer"}},
		{"guard is root-only", []string{"agents: {guard: true}\n"}, []string{"agents.guard is set in a layer"}},
		{"upgrade is root-only", []string{"agents: {upgrade: true}\n"}, []string{"agents.upgrade is set in a layer"}},
		{"sharedInstructions is root-only", []string{"agents: {sharedInstructions: true}\n"}, []string{"agents.sharedInstructions is set in a layer"}},
		{"codex settings are root-only", []string{"agents: {codex: {}}\n"}, []string{"agents.codex is set in a layer"}},
		{"opencode settings are root-only", []string{"agents: {opencode: {skillsDir: ~/s}}\n"}, []string{"agents.opencode is set in a layer"}},
		{"an empty prune key is still set", []string{"agents:\n  prune:\n"}, []string{"agents.prune is set in a layer"}},
		{"every root-only key is named", []string{"agents: {prune: true, guard: false}\n"}, []string{"agents.prune is set", "agents.guard is set"}},
		{"a marketplace in two files", []string{"agents: {marketplaces: {sai: owner/other}}\n"}, []string{"agents.marketplaces.sai is set in both", "{root}", "{l0}"}},
		{"an mcp server in two files", []string{"agents: {mcp: {root-srv: {command: /bin/true}}}\n"}, []string{"agents.mcp.root-srv is set in both"}},
		{"a profile in two files", []string{"agents: {profiles: {reviewer: {description: Again}}}\n"}, []string{"agents.profiles.reviewer is set in both"}},
		{"a marketplace in two layers", []string{"agents: {marketplaces: {w: owner/w}}\n", "agents: {marketplaces: {w: owner/v}}\n"}, []string{"agents.marketplaces.w is set in both", "{l0}", "{l1}"}},
		{"a plugin in two files", []string{"agents: {plugins: [humanize@sai]}\n"}, []string{"agents.plugins[1]", "humanize@sai is already declared by", "agents.plugins[0]", "{root}", "{l0}"}},
		{"a permission in two files", []string{"agents: {permissions: {allow: [{command: git status}]}}\n"}, []string{"agents.permissions.allow[1]", `"git status" duplicates`, "{root}", "{l0}"}},
		{"a permission decided twice", []string{"agents: {permissions: {deny: [{command: git status}]}}\n"}, []string{"is also listed in", "give each command one decision per provider"}},
		{"an instruction file in two files", []string{"agents: {instructions: '" + absShared + "'}\n", "agents: {instructions: '" + absShared + "'}\n"}, []string{"is already listed"}},
		{"a skill in two files", []string{"agents: {skills: [{path: elsewhere/root-skill}]}\n"}, []string{`skill "root-skill" is already declared by`, "{root}", "{l0}"}},
		{"a hook in two files", []string{"agents: {hooks: {commands: [{event: stop, command: /bin/true}]}}\n"}, []string{"duplicates", "agents.hooks.commands[0]"}},
		{"a plugin of an undeclared marketplace", []string{"agents: {plugins: [x@nowhere]}\n"}, []string{"agents.plugins[1]", "marketplace \"nowhere\"", "{l0}"}},
		{"a profile of an undeclared server", []string{"agents: {profiles: {p: {description: P, mcp: {nope: allow}}}}\n"}, []string{"agents.profiles.p", "mcp.nope: server is not declared in agents.mcp"}},
		{"a skill at the layer's own directory", []string{"agents: {skills: ['.']}\n"}, []string{"{l0}: agents.skills[", `path "." must point at a skill directory`}},
		{"a skill at the layer's parent", []string{"agents: {skills: ['..']}\n"}, []string{`path ".." must point at a skill directory`}},
		{"a blank instruction file", []string{"agents: {instructions: ['  ']}\n"}, []string{"{l0}: agents.instructions[", "file is required"}},
		{"a profile prompt with whitespace", []string{"agents: {profiles: {p: {description: P, prompt: ' a.md'}}}\n"}, []string{"{l0}: agents.profiles.p", "must be a file relative to the directory holding zakwas.yaml"}},
		{"an invalid mcp server names its file", []string{"agents: {mcp: {bad: {}}}\n"}, []string{"{l0}: agents.mcp.bad", "set command for a stdio server or url"}},
		{"an invalid profile names its file", []string{"agents: {profiles: {Bad: {description: x}}}\n"}, []string{"{l0}: agents.profiles.Bad"}},
		{"a marketplace without a source names its file", []string{"agents: {marketplaces: {nosrc: {providers: [claude]}}}\n"}, []string{"{l0}: agents.marketplaces.nosrc", "source is required"}},
		{"a provider outside the main config's", []string{"agents: {mcp: {codex-only: {command: /bin/true, providers: [codex]}}}\n"}, []string{"agents.mcp.codex-only", `provider "codex"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootBody := agentsRoot
			if strings.Contains(tt.name, "instruction file") {
				rootBody = strings.Replace(rootBody, "instructions: root.md", "instructions: '"+absShared+"'", 1)
			}
			c, root, layers, err := parseAgentsLayers(t, rootBody, tt.layers...)
			if err == nil {
				t.Fatalf("merged: %+v", c.Agents)
			}
			for _, want := range tt.want {
				want = strings.NewReplacer("{root}", root, "{l0}", layers[0].Path, "{l1}", layers[len(layers)-1].Path).Replace(want)
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestParseLayeredAgentsNamesBothFiles(t *testing.T) {
	root := writeConfig(t, agentsRoot)
	one := writeLayer(t, "one", "agents: {plugins: [humanize@sai]}\n")
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{one})
	if err == nil {
		t.Fatal("merged")
	}
	for _, f := range []string{root, one.Path} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error does not name %s:\n%v", f, err)
		}
	}
}

func TestParseLayeredAgentsLayerMayUseTheMainConfigsMarketplace(t *testing.T) {
	_, _, _, err := parseAgentsLayers(t, agentsRoot, "agents: {plugins: [extra@sai]}\n")
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseLayeredAgentsNeedsTheMainConfigsAgents(t *testing.T) {
	_, _, layers, err := parseAgentsLayers(t, "files: [{src: a, dst: ~/.a}]\n", "agents: {plugins: [x@y]}\n")
	if err == nil || !strings.Contains(err.Error(), layers[0].Path+" sets agents, but the main zakwas.yaml has no agents section") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseLayeredAgentsLayerOrder(t *testing.T) {
	c, _, _, err := parseAgentsLayers(t, agentsRoot,
		"agents: {plugins: [b@sai], instructions: '"+absOne+"'}\n",
		"agents: {plugins: [a@sai], instructions: '"+absTwo+"'}\n")
	if err != nil {
		t.Fatal(err)
	}
	var plugins []string
	for _, p := range c.Agents.Plugins {
		plugins = append(plugins, p.ID)
	}
	if got, want := strings.Join(plugins, " "), "humanize@sai b@sai a@sai"; got != want {
		t.Errorf("plugins = %q, want registration order: %q", got, want)
	}
	if got, want := c.Agents.Instructions[1:], []string{absOne, absTwo}; !slices.Equal(got, want) {
		t.Errorf("instructions = %q, want %q", got, want)
	}
}

func TestParseLayeredAgentsOptInProviders(t *testing.T) {
	const layer = "agents: {marketplaces: {work: {source: owner/work, providers: [codex]}}}\n"
	c, _, _, err := parseAgentsLayers(t, "agents: {guard: false, mcp: {srv: {command: echo}}}\n", layer)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Agents.Names(ProviderCodex) || len(c.Agents.MCPFor(ProviderCodex)) != 1 {
		t.Error("a layer entry naming codex does not opt codex in for the merged agents config, as in a single file")
	}
	_, _, _, err = parseAgentsLayers(t, "agents: {guard: false, providers: [claude], mcp: {srv: {command: echo}}}\n", layer)
	if err == nil || !strings.Contains(err.Error(), `provider "codex" is not in agents.providers`) {
		t.Errorf("err = %v, want the main config's providers to stay the limit", err)
	}
}
