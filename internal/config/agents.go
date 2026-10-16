package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Agent providers, all converged.
const (
	ProviderClaude   = "claude"
	ProviderCodex    = "codex"
	ProviderOpencode = "opencode"
	ProviderGemini   = "gemini"
	ProviderCopilot  = "copilot"
)

// Providers lists every known provider, in the order they are planned.
var Providers = []string{ProviderClaude, ProviderCodex, ProviderOpencode, ProviderGemini, ProviderCopilot}

// OptIn reports whether provider is converged only when zakwas.yaml names it,
// so configs that predate it leave it alone.
func OptIn(provider string) bool {
	return provider == ProviderCodex || provider == ProviderGemini || provider == ProviderCopilot
}

// Agents declares agent plugin marketplaces and plugins once, for every
// provider they target.
type Agents struct {
	Codex              *CodexSettings         `yaml:"codex" jsonschema_description:"Global Codex configuration, applied independently of profiles. Requires explicitly opting in to codex."`
	Providers          []string               `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers zakwas manages: claude, codex, opencode, gemini, copilot. Marketplaces target these unless they set their own 'providers'; prune only touches these. codex, gemini and copilot are converged only when named here or in an entry's 'providers'; gemini has no plugin marketplaces, so only its instructions, MCP servers, skills, hooks, permissions and profiles are converged. A provider whose CLI is missing fails its plan; the others still converge. Default: claude, codex and opencode, with codex, gemini and copilot opt-in."`
	Upgrade            bool                   `yaml:"upgrade" jsonschema:"default=false" jsonschema_description:"Update installed plugins to the version their marketplace offers on apply. Run 'zakwas upgrade' to refresh the marketplaces first. Copilot CLI reports no plugin versions, so 'zakwas upgrade' updates its plugins itself, after refreshing the marketplaces. Default false."`
	Prune              bool                   `yaml:"prune" jsonschema:"default=false" jsonschema_description:"Remove user-scope plugins and marketplaces that are not declared here, for the managed providers. Plugins installed for a single project, marketplaces they still use, and marketplaces not declared in the provider's user settings are never touched. Removals are shown in 'zakwas plan' first. Default false."`
	Marketplaces       map[string]Marketplace `yaml:"marketplaces" jsonschema_description:"Marketplaces by name: the name must match the one in the marketplace's own manifest. The value is the source (GitHub 'owner/repo', a git URL, or a local path starting with './', '../', '~/' or '/'; relative paths resolve from the directory holding zakwas.yaml), or an object with 'source' and 'providers'."`
	Plugins            []Plugin               `yaml:"plugins" jsonschema_description:"Plugins to install and enable, each 'name@marketplace' with the marketplace declared under 'marketplaces', or an object with 'id' and 'providers'. A plugin targets its marketplace's providers unless it sets its own."`
	Instructions       InstructionFiles       `yaml:"instructions" jsonschema_description:"Global instructions: one file in the config repo, or a list joined in order, each relative to the directory holding zakwas.yaml (or absolute). Every file is a template that sees '{{ .Provider }}' (claude, codex, opencode, gemini, copilot or shared), '{{ .Home }}' and the templates.vars as '{{ .Vars }}'. Each managed provider gets its own rendered, protected copy: claude ${CLAUDE_CONFIG_DIR:-~/.claude}/CLAUDE.md, codex ${CODEX_HOME:-~/.codex}/AGENTS.md (only when codex is named, like its plugins), opencode ~/.config/opencode/AGENTS.md (best effort unless opencode is named), gemini the context file of its zakwas extension, ${GEMINI_CLI_HOME:-~}/.gemini/zakwas/GEMINI.md (only when gemini is named; ~/.gemini/GEMINI.md is never written), copilot ${COPILOT_HOME:-~/.copilot}/instructions/zakwas.instructions.md (only when copilot is named, with 'applyTo: \"**\"' added). Claude keeps its '@path' imports, the others get them inlined; a missing import fails the plan, as does a copy over the provider's size limit (codex: 32768 bytes). A file already there is backed up, a link zakwas made is replaced; copies of providers no longer managed are removed."`
	SharedInstructions bool                   `yaml:"sharedInstructions" jsonschema:"default=false" jsonschema_description:"Also write the rendered instructions, as provider 'shared', to ~/.agents/AGENTS.md, which Warp, Cline and Factory Droid read, and which Claude, Codex and opencode don't. Needs 'instructions'. Default false."`
	Hooks              *AgentHooks            `yaml:"hooks" jsonschema_description:"Global agent hooks. Commands are declared once and registered with each selected provider; klaudiush remains an optional managed integration."`
	Permissions        *AgentPermissions      `yaml:"permissions" jsonschema_description:"Shell command permissions declared once for the managed agents: command prefixes the agents may run without asking (allow), must have reviewed (review) or must never run (deny). Written to Claude Code's settings.json, a Codex rules file and a Gemini CLI user policy file, and to opencode through the agentPermissions template function. Copilot CLI has no user-level shell rules (only root-owned managed settings), so the plan reports them as unsupported for copilot."`
	MCP                map[string]MCPServer   `yaml:"mcp" jsonschema_description:"MCP servers by name, each a stdio server ('command') or an HTTP server ('url'), declared once for the managed agents. Registered at user scope in Claude Code (~/.claude.json), Codex (config.toml, edited in place) and Copilot CLI (${COPILOT_HOME:-~/.copilot}/mcp-config.json, edited in place), in the zakwas extension of Gemini CLI, and in opencode through the agentMCP template function. Secrets are never written: name the environment variables that hold them. A server of the same name that zakwas did not add is adopted when identical and fails the plan when it differs; removing a server here removes only servers zakwas added."`
	Profiles           map[string]Profile     `yaml:"profiles" jsonschema_description:"Agent profiles by name, each declared once and rendered for every managed provider: a Claude Code subagent file in ${CLAUDE_CONFIG_DIR:-~/.claude}/agents/zakwas/<name>.md (start it with 'claude --agent <name>'), a Codex profile file ${CODEX_HOME:-~/.codex}/<name>.config.toml ('codex --profile <name>', only when codex is named) an opencode agent file ~/.config/opencode/agents/<name>.md ('opencode --agent <name>', best effort unless opencode is named) a Gemini CLI subagent file ${GEMINI_CLI_HOME:-~}/.gemini/zakwas/agents/<name>.md (delegate with '@<name>', only when gemini is named) and a Copilot CLI agent file ${COPILOT_HOME:-~/.copilot}/agents/<name>.agent.md ('copilot --agent <name>', only when copilot is named). A profile denies every server declared under 'mcp' that it does not list; servers added outside zakwas are not affected. The plan names, per provider, every profile field that provider cannot express. A profile name may be at most 64 characters, and a profile name an opencode template also defines under 'agent' fails the plan, as does a profile file that a 'files', 'links' or 'templates' destination also writes. Removing a profile removes only the files zakwas rendered."`
	Skills             []Skill                `yaml:"skills" jsonschema_description:"Standalone skills, which don't come from a plugin marketplace, linked into every managed agent's skills directory: Claude Code's ${CLAUDE_CONFIG_DIR:-~/.claude}/skills, and ~/.agents/skills for Codex, opencode and Gemini CLI (${GEMINI_CLI_HOME:-~}/.agents/skills when GEMINI_CLI_HOME is set), and for Copilot CLI too unless COPILOT_HOME is set, which then reads ${COPILOT_HOME}/skills only. Each entry is a skill directory (holding SKILL.md) in the config repo or elsewhere on disk, or an object with 'git', 'path' and optional 'ref' for a skill inside a git repository. The directory name must equal the name in SKILL.md, and names are unique. zakwas replaces nothing it did not link; a skill directory or link already there fails the plan. 'plan' never fetches, 'apply' clones missing git sources and 'zakwas upgrade' pulls them."`
	Opencode           *Opencode              `yaml:"opencode" jsonschema_description:"Settings of the opencode provider."`
	Guard              *bool                  `yaml:"guard" jsonschema:"default=true" jsonschema_description:"Stop the managed agents from applying this config themselves. An agent that can run 'zakwas apply' can widen its own permissions, add hooks or install marketplaces, because apply writes agent config files the agent may not edit. While on, every managed agent is denied 'zakwas apply', 'upgrade', 'self-update', 'approve' and 'mcp' (Claude Code in settings.json, Codex in its rules file, Gemini CLI in its user policy file, opencode through the agentPermissions template function), Claude Code is denied edits to the config repo's main checkout and to zakwas state, and 'zakwas apply' and 'upgrade' started from an agent session refuse. 'plan' and 'check' stay allowed. The plan names a managed provider that cannot be protected, such as Copilot CLI, which has no user-level deny rules and is covered only by the refusal. Default true; false removes the rules zakwas added."`
}

// InstructionFiles is agents.instructions. In YAML it is either one file or
// a list of files.
type InstructionFiles []string

func (f *InstructionFiles) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*f = nil
		if one != "" {
			*f = InstructionFiles{one}
		}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := n.Decode(&list); err != nil {
			return err
		}
		*f = list
		return nil
	}
	return fmt.Errorf("line %d: instructions must be a file or a list of files", n.Line)
}

// AgentHooks declares global command hooks and optional integrations.
type AgentHooks struct {
	Klaudiush bool               `yaml:"klaudiush" jsonschema:"default=false" jsonschema_description:"Register klaudiush global hooks for its enabled providers. Zakwas checks registration without running klaudiush during plan and runs 'klaudiush init --install-hooks --global' during apply when needed. Default false."`
	Commands  []AgentCommandHook `yaml:"commands" jsonschema_description:"Command hooks declared once for the managed agents. Each command receives provider-specific event JSON on stdin; outputs and blocking behavior remain provider-specific."`
}

// AgentCommandHook is one command registered at an agent lifecycle event.
type AgentCommandHook struct {
	Event     string   `yaml:"event" jsonschema:"required,enum=beforeTool,enum=afterTool,enum=sessionStart,enum=stop" jsonschema_description:"Shared lifecycle event: beforeTool, afterTool, sessionStart, or stop. Zakwas maps it to each provider's native event."`
	Command   string   `yaml:"command" jsonschema:"required,minLength=1" jsonschema_description:"Shell command to run. Use an absolute executable path when the agent may have a different PATH; the command receives provider-specific event JSON on stdin."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers for this hook; must be in agents.providers. Default: agents.providers, with Codex, Gemini and Copilot opt-in as for plugins."`
}

// AgentPermissions lists shell command prefixes by decision.
type AgentPermissions struct {
	Allow  []AgentPermission `yaml:"allow" jsonschema_description:"Commands the agents run without asking: Claude Code permissions.allow, Codex decision allow (runs outside the sandbox without approval), Gemini CLI decision allow, opencode allow."`
	Review []AgentPermission `yaml:"review" jsonschema_description:"Commands that need a review before they run: Codex decision prompt, Gemini CLI decision ask_user, opencode ask. Not written for Claude Code, whose auto mode already reviews every command it doesn't allow."`
	Deny   []AgentPermission `yaml:"deny" jsonschema_description:"Commands the agents must never run: Claude Code permissions.deny, Codex decision forbidden, Gemini CLI decision deny, opencode deny."`
}

// AgentPermission is one command prefix. In YAML it is either the command
// string or a mapping.
type AgentPermission struct {
	Command       string   `yaml:"command" jsonschema:"required,minLength=1" jsonschema_description:"Command prefix: one or more words separated by spaces, matched on whole words, e.g. 'git status' matches 'git status --short' but not 'git statusx'. No shell operators, quotes, globs or variables."`
	Providers     []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers for this entry; must be in agents.providers. Default: agents.providers, with Codex, Gemini and Copilot opt-in as for plugins."`
	Justification string   `yaml:"justification" jsonschema:"minLength=1,pattern=\\S" jsonschema_description:"One-line reason Codex shows the agent when the rule prompts for or forbids a command, e.g. 'use git push --force-with-lease'. Gemini CLI shows it for deny rules. Other providers ignore it."`
}

// MCPServer is one MCP server: a stdio server when it has a command, an
// HTTP server when it has a url. Secrets are referenced by the name of an
// environment variable, which each agent resolves its own way.
type MCPServer struct {
	Command        string            `yaml:"command" jsonschema:"minLength=1" jsonschema_description:"Command that starts a stdio server. A leading ~ (alone or before /) is expanded to your home directory, so ~/.local/bin/x works on any machine; ~user and $HOME are not expanded. Set either 'command' or 'url'."`
	Args           []string          `yaml:"args" jsonschema_description:"Arguments of the command. An argument that is ~ (quoted in YAML, since a bare ~ is null) or starts with ~/ is expanded to your home directory; a ~ elsewhere in it (--dir=~/x), ~user and $HOME are not. Otherwise written to the agents' config as is, so keep secrets out of them."`
	Env            map[string]string `yaml:"env" jsonschema_description:"Environment variables set when the command starts, with their values. A value that starts with ~/ is expanded to your home directory (a bare ~, ~user and $HOME are not). Otherwise written to the agents' config as is, so list secrets under 'envVars' instead."`
	EnvVars        []string          `yaml:"envVars" jsonschema:"uniqueItems=true" jsonschema_description:"Names of environment variables passed through from the agent's own environment to the command: Claude Code env NAME=${NAME}, Codex env_vars, Gemini CLI env NAME=$NAME, opencode environment NAME={env:NAME}. Gemini CLI expands $NAME itself, so a server whose command, args, url, env, headers or oauth settings hold a literal '$', or whose name has an underscore, is left out of Gemini CLI and the plan says so."`
	URL            string            `yaml:"url" jsonschema:"minLength=1" jsonschema_description:"URL of an HTTP (streamable) server, http:// or https://, without a user name or password. Written as is, so keep keys out of the query. Set either 'command' or 'url'."`
	Headers        map[string]string `yaml:"headers" jsonschema_description:"HTTP headers with their values. Written to the agents' config as is, so use 'headerEnv' or 'bearerTokenEnv' for secrets."`
	HeaderEnv      map[string]string `yaml:"headerEnv" jsonschema_description:"HTTP headers whose value is read from an environment variable: header name to variable name."`
	BearerTokenEnv string            `yaml:"bearerTokenEnv" jsonschema:"minLength=1" jsonschema_description:"Name of the environment variable holding a bearer token, sent as 'Authorization: Bearer <token>'."`
	OAuth          MCPOAuth          `yaml:"oauth" jsonschema:"default=false" jsonschema_description:"The server signs in with OAuth. true only adds the login command to plan and apply, because zakwas never opens a browser; the server registers its own client. A mapping with 'clientId' (and optionally 'callbackPort' and 'scope') also registers that pre-registered public client with every provider that can take it, for a gateway that has no dynamic client registration. Not combined with bearerTokenEnv or an Authorization header. Default false."`
	Providers      []string          `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers to register this server with; must be in agents.providers. Default: agents.providers, with Codex, Gemini and Copilot opt-in as for plugins."`
}

// IsHTTP reports whether the server is reached by URL rather than started.
func (s MCPServer) IsHTTP() bool { return s.URL != "" }

// MCPApplyTool is the zakwas MCP tool that applies a proposal.
const MCPApplyTool = "zakwas_apply"

// PromptTools are the tools of the server that every agent must ask the
// user about on each call, in the providers that can ask per tool. The
// only one is MCPApplyTool of a `zakwas mcp --enable-apply` server.
func (s MCPServer) PromptTools() []string {
	if s.IsHTTP() || filepath.Base(s.Command) != "zakwas" || len(s.Args) == 0 || s.Args[0] != "mcp" {
		return nil
	}
	for _, arg := range s.Args[1:] {
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name != "enable-apply" || !strings.HasPrefix(arg, "-") {
			continue
		}
		if on, err := strconv.ParseBool(value); !hasValue || err == nil && on {
			return []string{MCPApplyTool}
		}
	}
	return nil
}

// NamedMCPServer is one agents.mcp entry resolved for a provider.
type NamedMCPServer struct {
	Name string
	MCPServer
}

// MCPFor returns the agents.mcp servers targeting provider, sorted by name.
// Like plugins, a server without its own providers reaches Codex only when
// zakwas.yaml names codex.
func (a *Agents) MCPFor(provider string) []NamedMCPServer {
	if a == nil {
		return nil
	}
	var out []NamedMCPServer
	for _, name := range slices.Sorted(maps.Keys(a.MCP)) {
		s := a.MCP[name]
		if slices.Contains(a.targets(s.Providers), provider) {
			out = append(out, NamedMCPServer{Name: name, MCPServer: s})
		}
	}
	return out
}

// Profile is one agents.profiles entry: what a profile may do, declared once
// for every provider.
type Profile struct {
	Description string            `yaml:"description" jsonschema:"required,minLength=1" jsonschema_description:"One line on what the profile is for. Claude Code and opencode show it; Codex profiles carry no description."`
	Prompt      string            `yaml:"prompt" jsonschema:"minLength=1" jsonschema_description:"File in the config repo (relative to the directory holding zakwas.yaml, or absolute) with the profile's instructions. A template that sees '{{ .Provider }}', '{{ .Home }}' and the templates.vars as '{{ .Vars }}'. Written as the body of the Claude Code and opencode agent files and as Codex developer_instructions. Claude Code replaces its default system prompt with it."`
	Tools       []string          `yaml:"tools" jsonschema:"uniqueItems=true,enum=read,enum=search,enum=edit,enum=shell,enum=web" jsonschema_description:"Abstract tools the profile may use: read, search (glob and grep), edit, shell, web. Omit for all of them. Claude Code gets a tools list and opencode denies the others; Codex cannot restrict tools."`
	Edit        string            `yaml:"edit" jsonschema:"enum=allow,enum=ask,enum=deny" jsonschema_description:"File edits: allow, ask or deny. Claude Code: acceptEdits, default, or the edit tools removed. opencode: its edit permission. Codex: sandbox read-only for deny, workspace-write for allow; ask is not expressible."`
	Shell       string            `yaml:"shell" jsonschema:"enum=allow,enum=ask,enum=deny" jsonschema_description:"Shell commands: allow, ask or deny. opencode: bash permission, with agents.permissions applied; Claude Code: ask is its default, deny removes Bash, allow is not expressible. Codex: approval_policy never for allow and on-request for ask; deny is not expressible."`
	Sandbox     string            `yaml:"sandbox" jsonschema:"enum=read-only,enum=workspace-write,enum=danger-full-access" jsonschema_description:"Codex sandbox mode. Default: read-only when edit is deny, workspace-write when it is allow. Claude Code and opencode have no sandbox."`
	Network     *bool             `yaml:"network" jsonschema_description:"Network access. Codex: sandbox_workspace_write.network_access. Claude Code and opencode: false removes the web tools, but their shell commands stay unrestricted."`
	Reviewer    string            `yaml:"reviewer" jsonschema:"enum=user,enum=auto" jsonschema_description:"Who approves what needs approval: user, or auto, an automatic reviewer. Claude Code: permission mode auto; Codex: approvals_reviewer auto_review; opencode has none."`
	MCP         map[string]string `yaml:"mcp" jsonschema_description:"Access to each MCP server declared under 'mcp': allow, ask or deny. A declared server not listed is denied. Codex: enabled and default_tools_approval_mode; Claude Code: the server's tools in or out of the profile (allow is not expressible, its tools ask); opencode: a '<server>_*' permission."`
	Providers   []string          `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers to render the profile for; must be in agents.providers. Default: agents.providers, with Codex, Gemini and Copilot opt-in as for plugins."`
	Claude      *ProfileProvider  `yaml:"claude" jsonschema_description:"Claude Code only: model and extra frontmatter."`
	Codex       *ProfileProvider  `yaml:"codex" jsonschema_description:"Codex only: model and extra profile settings."`
	Opencode    *ProfileProvider  `yaml:"opencode" jsonschema_description:"opencode only: model and extra frontmatter."`
	Gemini      *ProfileProvider  `yaml:"gemini" jsonschema_description:"Gemini CLI only: model and extra frontmatter of its subagent file, such as temperature or max_turns."`
	Copilot     *ProfileProvider  `yaml:"copilot" jsonschema_description:"Copilot CLI only: model and extra frontmatter of its agent file."`
}

// ProfileProvider is one provider's share of a profile.
type ProfileProvider struct {
	Model     string         `yaml:"model" jsonschema:"minLength=1" jsonschema_description:"Model for this provider, in its own spelling, e.g. 'sonnet' for Claude Code, 'gpt-5' for Codex, 'anthropic/claude-sonnet-4' for opencode."`
	Overrides map[string]any `yaml:"overrides" jsonschema_description:"Extra top-level frontmatter keys (Claude Code, opencode) or settings (Codex), written as is. Values are re-typed by YAML, so quote ones that must stay strings (dates, versions). A key zakwas sets from the profile's own fields is rejected."`
}

// Skill is one agents.skills entry. In YAML it is either the path of a local
// skill directory or a mapping.
type Skill struct {
	Path      string   `yaml:"path" jsonschema:"required,minLength=1" jsonschema_description:"Skill directory holding SKILL.md, named like the skill. Without 'git': relative to the directory holding zakwas.yaml, or absolute or '~/...'. With 'git': a relative path inside the repository."`
	Git       string   `yaml:"git" jsonschema:"minLength=1" jsonschema_description:"Git source of the skill: GitHub 'owner/repo' or a clone URL. zakwas clones it on apply into ~/.local/share/zakwas/skills and pulls it on 'zakwas upgrade'. Omit for a skill on disk."`
	Ref       string   `yaml:"ref" jsonschema:"minLength=1" jsonschema_description:"Branch or tag to clone, only with 'git'. Default: the repository's default branch."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=gemini,enum=copilot" jsonschema_description:"Providers to link this skill for; must be in agents.providers. Default: agents.providers, with Codex, Gemini and Copilot opt-in as for plugins. Codex, opencode and Copilot share ~/.agents/skills."`
}

func (s *Skill) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&s.Path)
	}
	if err := knownKeys(n, "a skill", "path", "git", "ref", "providers"); err != nil {
		return err
	}
	type plain Skill
	return n.Decode((*plain)(s))
}

// Name is the skill's name: the last element of its path, which is both the
// link's name and what SKILL.md must call the skill.
func (s Skill) Name() string { return filepath.Base(filepath.Clean(s.Path)) }

// NamedSkill is one agents.skills entry resolved for a provider.
type NamedSkill struct {
	Name string
	Skill
}

// SkillsFor returns the agents.skills targeting provider, sorted by name.
// Like plugins, a skill without its own providers reaches Codex only when
// zakwas.yaml names codex.
func (a *Agents) SkillsFor(provider string) []NamedSkill {
	if a == nil {
		return nil
	}
	var out []NamedSkill
	for _, s := range a.Skills {
		if slices.Contains(a.targets(s.Providers), provider) {
			out = append(out, NamedSkill{Name: s.Name(), Skill: s})
		}
	}
	slices.SortFunc(out, func(x, y NamedSkill) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// NamesInSkill reports whether a skill lists provider in its own providers,
// which makes the provider strict for skills.
func (a *Agents) NamesInSkill(provider string) bool {
	if a == nil {
		return false
	}
	for _, s := range a.Skills {
		if slices.Contains(s.Providers, provider) {
			return true
		}
	}
	return a.Names(provider)
}

// Profile tool categories, the values of Profile.Tools.
var ProfileTools = []string{"read", "search", "edit", "shell", "web"}

// Profile decisions, the values of edit, shell and the mcp entries.
var profileDecisions = []string{PermissionAllow, "ask", PermissionDeny}

// ProfileControlled are the keys a provider's file gets from the profile's
// own fields, which overrides must not set.
var ProfileControlled = map[string][]string{
	ProviderClaude:   {"name", "description", "model", "tools", "disallowedTools", "permissionMode"},
	ProviderCodex:    {"model", "approval_policy", "approvals_reviewer", "sandbox_mode", "sandbox_workspace_write", "developer_instructions", "mcp_servers"},
	ProviderOpencode: {"description", "model", "permission"},
	ProviderGemini:   {"name", "description", "kind", "model", "tools"},
	ProviderCopilot:  {"name", "description", "model", "tools"},
}

// NamedProfile is one agents.profiles entry resolved for a provider.
type NamedProfile struct {
	Name string
	Profile
}

// share is the provider's own block, nil when the profile has none.
func (p Profile) share(provider string) *ProfileProvider {
	switch provider {
	case ProviderClaude:
		return p.Claude
	case ProviderCodex:
		return p.Codex
	case ProviderOpencode:
		return p.Opencode
	case ProviderGemini:
		return p.Gemini
	case ProviderCopilot:
		return p.Copilot
	}
	return nil
}

// Share returns the provider's own block, an empty one when there is none.
func (p Profile) Share(provider string) ProfileProvider {
	if s := p.share(provider); s != nil {
		return *s
	}
	return ProfileProvider{}
}

// ProfilesFor returns the agents.profiles targeting provider, sorted by
// name. Like plugins, a profile without its own providers reaches Codex only
// when zakwas.yaml names codex.
func (a *Agents) ProfilesFor(provider string) []NamedProfile {
	if a == nil {
		return nil
	}
	var out []NamedProfile
	for _, name := range slices.Sorted(maps.Keys(a.Profiles)) {
		p := a.Profiles[name]
		if slices.Contains(a.targets(p.Providers), provider) {
			out = append(out, NamedProfile{Name: name, Profile: p})
		}
	}
	return out
}

// NamesInProfile reports whether a profile lists provider in its own
// providers, which makes the provider strict for profiles.
func (a *Agents) NamesInProfile(provider string) bool {
	if a == nil {
		return false
	}
	for _, p := range a.Profiles {
		if slices.Contains(p.Providers, provider) {
			return true
		}
	}
	return a.Names(provider)
}

// Permission decisions, the keys of agents.permissions.
const (
	PermissionAllow  = "allow"
	PermissionReview = "review"
	PermissionDeny   = "deny"
)

// Permission is one agents.permissions entry resolved for a provider.
type Permission struct {
	Decision      string
	Words         []string
	Justification string
}

// Command is the prefix with its words joined by single spaces.
func (p Permission) Command() string { return strings.Join(p.Words, " ") }

func (p *AgentPermission) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.Command)
	}
	if err := knownKeys(n, "a permission", "command", "providers", "justification"); err != nil {
		return err
	}
	type plain AgentPermission
	return n.Decode((*plain)(p))
}

// lists returns the entries by decision, in the order providers apply them.
func (p *AgentPermissions) lists() []struct {
	decision string
	entries  []AgentPermission
} {
	return []struct {
		decision string
		entries  []AgentPermission
	}{{PermissionAllow, p.Allow}, {PermissionReview, p.Review}, {PermissionDeny, p.Deny}}
}

// PermissionsFor returns the agents.permissions entries targeting provider:
// allow, then review, then deny, each in declared order. Like plugins, an
// entry without its own providers reaches Codex only when zakwas.yaml names
// codex.
func (a *Agents) PermissionsFor(provider string) []Permission {
	if a == nil || a.Permissions == nil {
		return nil
	}
	var out []Permission
	for _, list := range a.Permissions.lists() {
		for _, e := range list.entries {
			if slices.Contains(a.permissionProviders(e), provider) {
				out = append(out, Permission{Decision: list.decision, Words: strings.Fields(e.Command), Justification: e.Justification})
			}
		}
	}
	return out
}

// permissionProviders are the providers an entry targets: its own, or
// agents.providers, where Codex counts only when zakwas.yaml names it.
func (a *Agents) permissionProviders(e AgentPermission) []string {
	return a.targets(e.Providers)
}

// targets are the providers an entry targets: its own, or agents.providers,
// where Codex, Gemini and Copilot count only when zakwas.yaml names them.
func (a *Agents) targets(own []string) []string {
	if own != nil {
		return own
	}
	return slices.DeleteFunc(slices.Clone(a.DefaultProviders()), func(p string) bool { return OptIn(p) && !a.Names(p) })
}

// Wants reports whether provider is to be converged at all: an opt-in
// provider only when zakwas.yaml names it, in agents.providers or in the
// providers of an entry that has them; the others when managed.
func (a *Agents) Wants(provider string) bool {
	if a == nil || !a.Manages(provider) {
		return false
	}
	if !OptIn(provider) || a.Names(provider) {
		return true
	}
	if a.Hooks != nil {
		for _, h := range a.Hooks.Commands {
			if slices.Contains(h.Providers, provider) {
				return true
			}
		}
	}
	if a.Permissions != nil {
		for _, l := range a.Permissions.lists() {
			for _, e := range l.entries {
				if slices.Contains(e.Providers, provider) {
					return true
				}
			}
		}
	}
	for _, s := range a.MCP {
		if slices.Contains(s.Providers, provider) {
			return true
		}
	}
	return a.NamesInProfile(provider) || a.NamesInSkill(provider)
}

// GuardedCommands are the zakwas commands that change the system or zakwas
// itself, which the guard keeps agents from running. mcp is among them
// because with --enable-apply an agent that starts it in a shell is the
// client that answers its confirmation.
var GuardedCommands = []string{"apply", "upgrade", "self-update", "approve", "mcp"}

// GuardEnabled reports whether the guard is on: it is, for any agents
// section, unless it sets guard to false.
func (a *Agents) GuardEnabled() bool {
	return a != nil && (a.Guard == nil || *a.Guard)
}

// GuardsProvider reports whether the guard applies to provider: it is
// managed, and Codex is also named, like for permissions.
func (a *Agents) GuardsProvider(provider string) bool {
	return a.GuardEnabled() && slices.Contains(a.permissionProviders(AgentPermission{}), provider)
}

// GuardPermissions are the deny entries that keep provider's agents from
// running the guarded zakwas commands, none when the guard is off or the
// provider isn't guarded.
func (a *Agents) GuardPermissions(provider string) []Permission {
	if !a.GuardsProvider(provider) {
		return nil
	}
	out := make([]Permission, len(GuardedCommands))
	for i, c := range GuardedCommands {
		out[i] = Permission{
			Decision: PermissionDeny, Words: []string{"zakwas", c},
			Justification: "agents must not apply the zakwas config; ask the user to run it in a terminal",
		}
	}
	return out
}

// permissionMeta are the characters a shell or an agent's matcher would
// read as more than a plain word: operators, quoting, expansion, globs.
const permissionMeta = "*?[]{}()|&;<>$`'\"\\"

// Opencode configures the opencode provider, which has no plugin system:
// zakwas links each plugin's skills into its skills directory.
type Opencode struct {
	SkillsDir string `yaml:"skillsDir" jsonschema:"minLength=1" jsonschema_description:"Directory opencode loads user skills from, absolute or '~/...'. Default: $XDG_CONFIG_HOME/opencode/skills, or ~/.config/opencode/skills when it is unset. OPENCODE_CONFIG_DIR is ignored, since tools like Orca set it to a private config; set this instead to install elsewhere."`
}

// Marketplace is a plugin marketplace. In YAML it is either the source
// string or a mapping.
type Marketplace struct {
	Source    string   `yaml:"source" jsonschema:"required,minLength=1" jsonschema_description:"GitHub 'owner/repo', a git URL, or a local path starting with './', '../', '~/' or '/' (relative paths resolve from the directory holding zakwas.yaml)."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=copilot" jsonschema_description:"Providers to add this marketplace to; must be in agents.providers. Default: agents.providers."`
}

// Plugin is one plugin. In YAML it is either the "name@marketplace" string
// or a mapping.
type Plugin struct {
	ID        string   `yaml:"id" jsonschema:"required,minLength=1" jsonschema_description:"Plugin id 'name@marketplace'; the marketplace must be declared under agents.marketplaces."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode,enum=copilot" jsonschema_description:"Providers to install this plugin for; must be targeted by its marketplace. Default: the marketplace's providers."`
}

func (m *Marketplace) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&m.Source)
	}
	if err := knownKeys(n, "a marketplace", "source", "providers"); err != nil {
		return err
	}
	type plain Marketplace
	return n.Decode((*plain)(m))
}

func (p *Plugin) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.ID)
	}
	if err := knownKeys(n, "a plugin", "id", "providers"); err != nil {
		return err
	}
	type plain Plugin
	return n.Decode((*plain)(p))
}

// knownKeys rejects unknown mapping keys, which yaml's KnownFields doesn't
// check inside custom unmarshalers. The message leaves the key out: a secret
// pasted as a key name must not be echoed.
func knownKeys(n *yaml.Node, what string, keys ...string) error {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !slices.Contains(keys, k.Value) {
			return fmt.Errorf("line %d: unknown field in %s; allowed: %s", k.Line, what, strings.Join(keys, ", "))
		}
	}
	return nil
}

// SplitPluginID splits "name@marketplace".
func SplitPluginID(id string) (name, marketplace string, ok bool) {
	name, marketplace, ok = strings.Cut(id, "@")
	if !ok || name == "" || marketplace == "" || strings.Contains(marketplace, "@") {
		return "", "", false
	}
	return name, marketplace, true
}

// DefaultProviders is agents.providers, or every provider when unset.
func (a *Agents) DefaultProviders() []string {
	if a.Providers == nil {
		return Providers
	}
	return a.Providers
}

// Manages reports whether provider is in agents.providers.
func (a *Agents) Manages(provider string) bool {
	return slices.Contains(a.DefaultProviders(), provider)
}

// MarketplaceProviders returns the providers a declared marketplace targets.
func (a *Agents) MarketplaceProviders(name string) []string {
	if m, ok := a.Marketplaces[name]; ok && m.Providers != nil {
		return m.Providers
	}
	return pluginProviders(a.DefaultProviders())
}

// pluginProviders drops the providers without plugin marketplaces.
func pluginProviders(list []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(p string) bool { return p == ProviderGemini })
}

// PluginProviders returns the providers a plugin targets.
func (a *Agents) PluginProviders(p Plugin) []string {
	if p.Providers != nil {
		return p.Providers
	}
	_, mk, _ := SplitPluginID(p.ID)
	return a.MarketplaceProviders(mk)
}

// Names reports whether zakwas.yaml lists provider itself, in
// agents.providers or a marketplace's or plugin's providers, rather than
// getting it from the all-providers default.
func (a *Agents) Names(provider string) bool {
	if slices.Contains(a.Providers, provider) {
		return true
	}
	for _, m := range a.Marketplaces {
		if slices.Contains(m.Providers, provider) {
			return true
		}
	}
	for _, p := range a.Plugins {
		if slices.Contains(p.Providers, provider) {
			return true
		}
	}
	return false
}

// NamesProvider reports whether a plugin targets provider because the
// config lists it, in the plugin's, its marketplace's or agents.providers,
// rather than by the all-providers default.
func (a *Agents) NamesProvider(p Plugin, provider string) bool {
	_, mk, _ := SplitPluginID(p.ID)
	return slices.Contains(p.Providers, provider) ||
		slices.Contains(a.Marketplaces[mk].Providers, provider) ||
		slices.Contains(a.Providers, provider)
}

// MarketplaceNames returns the declared marketplace names, sorted.
func (a *Agents) MarketplaceNames() []string {
	names := make([]string, 0, len(a.Marketplaces))
	for n := range a.Marketplaces {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

var marketplaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (a *Agents) validate() []error {
	var errs []error
	checkProviders := func(what string, list, allowed []string, allowedWhat string) {
		if list != nil && len(list) == 0 {
			errs = append(errs, fmt.Errorf("%s: list at least one provider, or omit it for %s", what, allowedWhat))
		}
		for i, p := range list {
			switch {
			case slices.Contains(list[:i], p):
				errs = append(errs, fmt.Errorf("%s: provider %q is listed twice", what, p))
			case !slices.Contains(Providers, p):
				errs = append(errs, fmt.Errorf("%s: unknown provider %q (want %s)", what, p, strings.Join(Providers, ", ")))
			case !slices.Contains(allowed, p):
				errs = append(errs, fmt.Errorf("%s: provider %q is not in %s", what, p, allowedWhat))
			}
		}
	}
	checkProviders("agents.providers", a.Providers, Providers, "the default (all)")
	if (len(a.Marketplaces) > 0 || len(a.Plugins) > 0) && len(pluginProviders(a.DefaultProviders())) == 0 {
		errs = append(errs, errors.New("agents.marketplaces and agents.plugins: no provider in agents.providers has plugins (gemini has none); add claude, codex or opencode"))
	}
	folded := map[string]string{}
	for _, name := range a.MarketplaceNames() {
		m := a.Marketplaces[name]
		what := "agents.marketplaces." + name
		if other, dup := folded[strings.ToLower(name)]; dup {
			errs = append(errs, fmt.Errorf("%s: differs from %s only in case; macOS file names ignore case, so use distinct names", what, other))
		}
		folded[strings.ToLower(name)] = name
		if !marketplaceName.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: name may only contain letters, digits, '.', '_' and '-'", what))
		}
		if m.Source == "" {
			errs = append(errs, fmt.Errorf("%s: source is required", what))
		}
		if slices.Contains(m.Providers, ProviderGemini) {
			errs = append(errs, fmt.Errorf("%s.providers: gemini has no plugin marketplaces", what))
		}
		checkProviders(what+".providers", m.Providers, a.DefaultProviders(), "agents.providers")
	}
	for i, f := range a.Instructions {
		switch {
		case strings.TrimSpace(f) == "":
			errs = append(errs, fmt.Errorf("agents.instructions[%d]: file is required", i))
		case f == "~" || strings.HasPrefix(f, "~/"):
			errs = append(errs, fmt.Errorf("agents.instructions: %q must be relative to the directory holding zakwas.yaml, or absolute", f))
		case slices.Contains(a.Instructions[:i], f):
			errs = append(errs, fmt.Errorf("agents.instructions[%d]: %q is already listed", i, f))
		}
	}
	if a.SharedInstructions && len(a.Instructions) == 0 {
		errs = append(errs, errors.New("agents.sharedInstructions: needs agents.instructions"))
	}
	if a.Opencode != nil {
		dir := a.Opencode.SkillsDir
		if dir != "" && !filepath.IsAbs(dir) && !strings.HasPrefix(dir, "~/") {
			errs = append(errs, fmt.Errorf("agents.opencode.skillsDir: %q must be absolute or start with ~/", dir))
		}
	}
	if a.Hooks != nil {
		seenHooks := map[string]int{}
		for i, h := range a.Hooks.Commands {
			what := fmt.Sprintf("agents.hooks.commands[%d]", i)
			if !slices.Contains([]string{"beforeTool", "afterTool", "sessionStart", "stop"}, h.Event) {
				errs = append(errs, fmt.Errorf("%s.event: unknown event %q", what, h.Event))
			}
			if strings.TrimSpace(h.Command) == "" {
				errs = append(errs, fmt.Errorf("%s.command: command is required", what))
			}
			checkProviders(what+".providers", h.Providers, a.DefaultProviders(), "agents.providers")
			providers := h.Providers
			if providers == nil {
				providers = a.DefaultProviders()
			}
			for _, provider := range providers {
				key := provider + "\x00" + h.Event + "\x00" + h.Command
				if first, dup := seenHooks[key]; dup {
					errs = append(errs, fmt.Errorf("%s: duplicates agents.hooks.commands[%d] for %s", what, first, provider))
				}
				seenHooks[key] = i
			}
		}
	}
	errs = append(errs, a.validateCodexSettings()...)
	errs = append(errs, a.validatePermissions(checkProviders)...)
	errs = append(errs, a.validateMCP(checkProviders)...)
	errs = append(errs, a.validateProfiles(checkProviders)...)
	errs = append(errs, a.validateSkills(checkProviders)...)
	seen := map[string]int{}
	for i, p := range a.Plugins {
		what := fmt.Sprintf("agents.plugins[%d]", i)
		_, mk, ok := SplitPluginID(p.ID)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: id %q must be name@marketplace", what, p.ID))
			continue
		}
		if first, dup := seen[p.ID]; dup {
			errs = append(errs, fmt.Errorf("%s: %s is already declared by agents.plugins[%d]", what, p.ID, first))
		} else {
			seen[p.ID] = i
		}
		if _, ok := a.Marketplaces[mk]; !ok {
			errs = append(errs, fmt.Errorf("%s: marketplace %q of %s is not declared in agents.marketplaces", what, mk, p.ID))
			continue
		}
		if slices.Contains(p.Providers, ProviderGemini) {
			errs = append(errs, fmt.Errorf("%s.providers: gemini has no plugins", what))
		}
		checkProviders(what+".providers", p.Providers, a.MarketplaceProviders(mk), "the providers of marketplace "+mk)
	}
	return errs
}

func (a *Agents) validatePermissions(checkProviders func(what string, list, allowed []string, allowedWhat string)) []error {
	if a.Permissions == nil {
		return nil
	}
	var errs []error
	type entry struct {
		what, decision string
	}
	seen := map[string]entry{}
	for _, list := range a.Permissions.lists() {
		for i, p := range list.entries {
			what := fmt.Sprintf("agents.permissions.%s[%d]", list.decision, i)
			words := strings.Fields(p.Command)
			if len(words) == 0 {
				errs = append(errs, fmt.Errorf("%s: command is required", what))
				continue
			}
			if at := strings.IndexFunc(p.Command, func(r rune) bool {
				return strings.ContainsRune(permissionMeta, r) || (unicode.IsControl(r) && !unicode.IsSpace(r))
			}); at >= 0 {
				r, _ := utf8.DecodeRuneInString(p.Command[at:])
				errs = append(errs, fmt.Errorf("%s: %q contains %q; list a plain command prefix, without shell operators, quotes, globs or variables", what, p.Command, r))
				continue
			}
			switch {
			case strings.ContainsFunc(p.Justification, unicode.IsControl):
				errs = append(errs, fmt.Errorf("%s.justification: write it on one line, without control characters", what))
			case p.Justification != "" && strings.TrimSpace(p.Justification) == "":
				errs = append(errs, fmt.Errorf("%s.justification: is blank; write a reason or leave it out", what))
			}
			checkProviders(what+".providers", p.Providers, a.DefaultProviders(), "agents.providers")
			command := strings.Join(words, " ")
			for _, provider := range a.permissionProviders(p) {
				key := provider + "\x00" + command
				first, dup := seen[key]
				switch {
				case !dup:
					seen[key] = entry{what: what, decision: list.decision}
				case first.decision == list.decision:
					errs = append(errs, fmt.Errorf("%s: %q duplicates %s for %s", what, command, first.what, provider))
				default:
					errs = append(errs, fmt.Errorf("%s: %q is also listed in %s for %s; give each command one decision per provider", what, command, first.what, provider))
				}
			}
		}
	}
	return errs
}

var (
	mcpName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	envVarName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerToken = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
)

func (a *Agents) validateMCP(checkProviders func(what string, list, allowed []string, allowedWhat string)) []error {
	var errs []error
	folded := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(a.MCP)) {
		s := a.MCP[name]
		what := "agents.mcp." + name
		fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(what+": "+format, args...)) }
		if !mcpName.MatchString(name) {
			fail("name may only contain letters, digits, '_' and '-', and must start with a letter or digit")
		}
		if other, dup := folded[strings.ToLower(name)]; dup {
			fail("differs from %s only in case; use distinct names", other)
		}
		folded[strings.ToLower(name)] = name
		checkProviders(what+".providers", s.Providers, a.DefaultProviders(), "agents.providers")
		switch {
		case s.Command != "" && s.URL != "":
			fail("set either command or url, not both")
		case s.Command == "" && s.URL == "":
			fail("set command for a stdio server or url for an HTTP server")
		}
		if strings.TrimSpace(s.Command) != s.Command {
			fail("command %q has leading or trailing whitespace", s.Command)
		}
		type field struct {
			name string
			set  bool
		}
		if s.Command != "" {
			for _, f := range []field{{"headers", len(s.Headers) > 0}, {"headerEnv", len(s.HeaderEnv) > 0}, {"bearerTokenEnv", s.BearerTokenEnv != ""}, {"oauth", s.OAuth.On()}} {
				if f.set {
					fail("%s only applies to a server with a url", f.name)
				}
			}
		}
		if s.URL != "" {
			for _, f := range []field{{"args", len(s.Args) > 0}, {"env", len(s.Env) > 0}, {"envVars", len(s.EnvVars) > 0}} {
				if f.set {
					fail("%s only applies to a server with a command", f.name)
				}
			}
			if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				fail("url %q must be an http:// or https:// URL", s.URL)
			} else if u.User != nil {
				fail("url holds a user name or password; use bearerTokenEnv, headerEnv or headers instead")
			}
		}
		for key := range s.Env {
			if !envVarName.MatchString(key) {
				fail("env name %q is not a valid environment variable name", key)
			}
		}
		for i, v := range s.EnvVars {
			switch {
			case !envVarName.MatchString(v):
				fail("envVars[%d] %q is not a valid environment variable name", i, v)
			case slices.Contains(s.EnvVars[:i], v):
				fail("envVars lists %q twice", v)
			}
			if _, set := s.Env[v]; set {
				fail("%q is in both env and envVars", v)
			}
		}
		if s.BearerTokenEnv != "" && !envVarName.MatchString(s.BearerTokenEnv) {
			fail("bearerTokenEnv %q is not a valid environment variable name", s.BearerTokenEnv)
		}
		headers := map[string]string{}
		addHeader := func(field, header string) {
			if !headerToken.MatchString(header) {
				fail("%s name %q is not a valid HTTP header name", field, header)
			}
			if other, dup := headers[strings.ToLower(header)]; dup {
				fail("header %q is set in both %s and %s", header, other, field)
			}
			headers[strings.ToLower(header)] = field
		}
		for _, h := range slices.Sorted(maps.Keys(s.Headers)) {
			addHeader("headers", h)
		}
		for _, h := range slices.Sorted(maps.Keys(s.HeaderEnv)) {
			addHeader("headerEnv", h)
			if !envVarName.MatchString(s.HeaderEnv[h]) {
				fail("headerEnv %q: %q is not a valid environment variable name", h, s.HeaderEnv[h])
			}
		}
		if s.BearerTokenEnv != "" {
			addHeader("bearerTokenEnv", "Authorization")
		}
		if s.OAuth.On() && headers["authorization"] != "" {
			fail("oauth can't be combined with an Authorization header (%s)", headers["authorization"])
		}
		for _, problem := range s.OAuth.Problems() {
			fail("%s", problem)
		}
		for _, text := range append(append(slices.Collect(maps.Values(s.Env)), slices.Collect(maps.Values(s.Headers))...), append([]string{s.Command}, s.Args...)...) {
			if strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\t' }) || !utf8.ValidString(text) {
				fail("values may not hold control characters or invalid UTF-8")
				break
			}
		}
	}
	return errs
}

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// MaxProfileName keeps a profile's file name, with the longest suffix a
// provider adds, within every file system's limit and each provider's own
// limit on agent names.
const MaxProfileName = 64

// rejectNullProfileNames fails a profile declared under a null key (null: or
// ~:), which the decoder drops without a trace, however the mapping is
// reached: directly, through an alias or through a merge key.
func rejectNullProfileNames(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	profiles := mappingValue(mappingValue(doc.Content[0], "agents"), "profiles")
	if line := nullKeyLine(profiles, 0); line > 0 {
		return fmt.Errorf("line %d: agents.profiles: a profile cannot be named null; quote the name (\"null\") or choose another", line)
	}
	return nil
}

const maxMergeDepth = 10000

func resolveAlias(n *yaml.Node) *yaml.Node {
	for depth := 0; n != nil && n.Kind == yaml.AliasNode && depth <= maxMergeDepth; depth++ {
		n = n.Alias
	}
	if n != nil && n.Kind == yaml.AliasNode {
		return nil
	}
	return n
}

func isNullKey(k *yaml.Node) bool {
	k = resolveAlias(k)
	return k != nil && k.Kind == yaml.ScalarNode && k.ShortTag() == "!!null"
}

func isMergeKey(k *yaml.Node) bool {
	return k != nil && k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge"
}

func nullKeyLine(n *yaml.Node, depth int) int {
	n = resolveAlias(n)
	if n == nil || depth > maxMergeDepth {
		return 0
	}
	switch n.Kind {
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if line := nullKeyLine(item, depth+1); line > 0 {
				return line
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			switch {
			case isNullKey(k):
				return k.Line
			case isMergeKey(k):
				if line := nullKeyLine(n.Content[i+1], depth+1); line > 0 {
					return line
				}
			}
		}
	}
	return 0
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	return lookupKey(n, key, 0)
}

func lookupKey(n *yaml.Node, key string, depth int) *yaml.Node {
	n = resolveAlias(n)
	if n == nil || n.Kind != yaml.MappingNode || depth > maxMergeDepth {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := resolveAlias(n.Content[i])
		if k != nil && k.Kind == yaml.ScalarNode && k.Value == key && !isMergeKey(k) {
			return n.Content[i+1]
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !isMergeKey(n.Content[i]) {
			continue
		}
		merged := resolveAlias(n.Content[i+1])
		if merged == nil {
			continue
		}
		sources := []*yaml.Node{merged}
		if merged.Kind == yaml.SequenceNode {
			sources = merged.Content
		}
		for _, src := range sources {
			if v := lookupKey(src, key, depth+1); v != nil {
				return v
			}
		}
	}
	return nil
}

func (a *Agents) validateProfiles(checkProviders func(what string, list, allowed []string, allowedWhat string)) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(a.Profiles)) {
		p := a.Profiles[name]
		what := "agents.profiles." + name
		fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(what+": "+format, args...)) }
		if !profileName.MatchString(name) {
			fail("name may only contain lowercase letters, digits and '-', and must start with a letter")
		}
		if len(name) > MaxProfileName {
			fail("name is %d characters long; at most %d are allowed", len(name), MaxProfileName)
		}
		switch {
		case strings.TrimSpace(p.Description) == "":
			fail("description is required")
		case strings.ContainsFunc(p.Description, unicode.IsControl):
			fail("description must be one line, without control characters")
		}
		if p.Prompt != "" && (strings.TrimSpace(p.Prompt) != p.Prompt || p.Prompt == "~" || strings.HasPrefix(p.Prompt, "~/")) {
			fail("prompt %q must be a file relative to the directory holding zakwas.yaml, or absolute", p.Prompt)
		}
		for i, t := range p.Tools {
			switch {
			case !slices.Contains(ProfileTools, t):
				fail("tools[%d]: unknown tool %q (want %s)", i, t, strings.Join(ProfileTools, ", "))
			case slices.Contains(p.Tools[:i], t):
				fail("tools lists %q twice", t)
			}
		}
		if p.Tools != nil && len(p.Tools) == 0 {
			fail("list at least one tool, or omit tools for all of them")
		}
		decision := func(field, v string) {
			if v != "" && !slices.Contains(profileDecisions, v) {
				fail("%s %q is not one of %s", field, v, strings.Join(profileDecisions, ", "))
			}
		}
		decision("edit", p.Edit)
		decision("shell", p.Shell)
		for _, c := range []struct{ tool, decision string }{{"edit", p.Edit}, {"shell", p.Shell}} {
			if (c.decision == PermissionAllow || c.decision == "ask") && p.Tools != nil && !slices.Contains(p.Tools, c.tool) {
				fail("%s is %s but tools leaves out %s", c.tool, c.decision, c.tool)
			}
		}
		if p.Sandbox != "" && !slices.Contains([]string{"read-only", "workspace-write", "danger-full-access"}, p.Sandbox) {
			fail("sandbox %q is not one of read-only, workspace-write, danger-full-access", p.Sandbox)
		}
		if p.Reviewer != "" && p.Reviewer != "user" && p.Reviewer != "auto" {
			fail("reviewer %q is not one of user, auto", p.Reviewer)
		}
		for _, server := range slices.Sorted(maps.Keys(p.MCP)) {
			if _, ok := a.MCP[server]; !ok {
				fail("mcp.%s: server is not declared in agents.mcp", server)
			}
			decision("mcp."+server, p.MCP[server])
			if p.MCP[server] == "" {
				fail("mcp.%s: set allow, ask or deny", server)
			}
		}
		checkProviders(what+".providers", p.Providers, a.DefaultProviders(), "agents.providers")
		for _, provider := range Providers {
			if p.share(provider) == nil {
				continue
			}
			share := p.Share(provider)
			if share.Model == "" && len(share.Overrides) == 0 {
				fail("%s: set a model or overrides, or remove it", provider)
			}
			if !slices.Contains(a.targets(p.Providers), provider) || !a.Manages(provider) {
				hint := "name it in agents.providers or the profile's providers"
				if OptIn(provider) {
					hint = strings.ToUpper(provider[:1]) + provider[1:] + " is opt-in, so " + hint
				}
				fail("%s: the profile is not rendered for %s; %s", provider, provider, hint)
			}
			if strings.ContainsFunc(share.Model, unicode.IsControl) || strings.TrimSpace(share.Model) != share.Model {
				fail("%s.model %q must be one line without surrounding whitespace", provider, share.Model)
			}
			for key := range share.Overrides {
				if key == "" {
					fail("%s.overrides: empty key", provider)
				}
				if slices.Contains(ProfileControlled[provider], key) {
					fail("%s.overrides.%s: zakwas sets it from the profile's own fields", provider, key)
				}
			}
		}
	}
	return errs
}

var skillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (a *Agents) validateSkills(checkProviders func(what string, list, allowed []string, allowedWhat string)) []error {
	var errs []error
	folded := map[string]int{}
	for i, s := range a.Skills {
		what := fmt.Sprintf("agents.skills[%d]", i)
		fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(what+": "+format, args...)) }
		word := func(field, v string) bool {
			if strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
				fail("%s %q must not contain spaces or control characters", field, v)
				return false
			}
			return true
		}
		clean := filepath.Clean(s.Path)
		switch {
		case strings.TrimSpace(s.Path) == "":
			fail("path is required")
			continue
		case strings.TrimSpace(s.Path) != s.Path || strings.ContainsFunc(s.Path, unicode.IsControl):
			fail("path %q must not have leading or trailing whitespace or control characters", s.Path)
			continue
		case s.Path == "~" || clean == "." || clean == "..":
			fail("path %q must point at a skill directory", s.Path)
			continue
		}
		if s.Git == "" {
			if s.Ref != "" {
				fail("ref only applies to a skill with git")
			}
		} else {
			switch {
			case !word("git", s.Git):
			case strings.HasPrefix(s.Git, "-") || strings.Contains(s.Git, "#"):
				fail("git %q must be a repository, without a leading '-' or a '#ref'; set ref instead", s.Git)
			}
			switch {
			case filepath.IsAbs(s.Path) || strings.HasPrefix(s.Path, "~") || clean == ".." || strings.HasPrefix(clean, "../"):
				fail("path %q must be relative to the root of the git repository", s.Path)
			case slices.ContainsFunc(strings.Split(clean, "/"), func(e string) bool { return strings.EqualFold(e, ".git") }):
				fail("path %q goes into .git", s.Path)
			}
			if s.Ref != "" && word("ref", s.Ref) && strings.HasPrefix(s.Ref, "-") {
				fail("ref %q must not start with '-'", s.Ref)
			}
		}
		name := s.Name()
		if !skillName.MatchString(name) {
			fail("name %q (the last element of path) may only contain letters, digits, '.', '_' and '-', and must start with a letter or digit", name)
		}
		if first, dup := folded[strings.ToLower(name)]; dup {
			fail("skill %q is already declared by agents.skills[%d]; skill names are compared without case, since macOS file names are", name, first)
		}
		folded[strings.ToLower(name)] = i
		checkProviders(what+".providers", s.Providers, a.DefaultProviders(), "agents.providers")
	}
	return errs
}
