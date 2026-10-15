package agents

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/templates"
)

// ordered is a mapping that keeps the order keys were set in, for YAML
// frontmatter and opencode's last-match-wins permission rules.
type ordered struct {
	keys []string
	vals map[string]any
}

func (o *ordered) set(key string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *ordered) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range o.keys {
		var key, val yaml.Node
		if err := key.Encode(k); err != nil {
			return nil, err
		}
		if err := val.Encode(o.vals[k]); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &key, &val)
	}
	return n, nil
}

// profileTools are the provider's names for each abstract tool.
var (
	claudeToolNames = map[string][]string{
		"read": {"Read"}, "search": {"Glob", "Grep"}, "edit": {"Edit", "Write", "NotebookEdit"},
		"shell": {"Bash"}, "web": {"WebFetch", "WebSearch"},
	}
	copilotToolNames = map[string][]string{
		"read": {"read"}, "search": {"search"}, "edit": {"edit"},
		"shell": {"execute"}, "web": {"web"},
	}
	opencodeToolKeys = map[string][]string{
		"read": {"read"}, "search": {"glob", "grep", "list"}, "edit": {"edit"},
		"shell": {"bash"}, "web": {"webfetch", "websearch"},
	}
)

// profileToolSets splits the abstract tools into the ones the profile keeps
// and the ones it takes away, in the order of config.ProfileTools: those its
// tools list leaves out, edit when edit is deny, shell when shell is deny,
// and web when network is false.
func profileToolSets(p config.Profile) (kept, removed []string) {
	for _, t := range config.ProfileTools {
		drop := p.Tools != nil && !slices.Contains(p.Tools, t)
		switch t {
		case "edit":
			drop = drop || p.Edit == config.PermissionDeny
		case "shell":
			drop = drop || p.Shell == config.PermissionDeny
		case "web":
			drop = drop || (p.Network != nil && !*p.Network)
		}
		if drop {
			removed = append(removed, t)
		} else {
			kept = append(kept, t)
		}
	}
	return kept, removed
}

// profileAccess is the access a profile gives each MCP server registered for
// provider: what it lists, deny for the rest.
func (m *Module) profileAccess(provider string, p config.Profile) (names []string, access map[string]string) {
	access = map[string]string{}
	for _, s := range m.Agents.MCPFor(provider) {
		names = append(names, s.Name)
		access[s.Name] = config.PermissionDeny
		if d := p.MCP[s.Name]; d != "" {
			access[s.Name] = d
		}
	}
	return names, access
}

func toolNames(tools []string, by map[string][]string) []string {
	var out []string
	for _, t := range tools {
		out = append(out, by[t]...)
	}
	return out
}

// profileFile is one provider's rendering of a profile: the file's content,
// without the prompt, and the profile fields the provider cannot express.
type profileFile struct {
	front       *ordered
	table       map[string]any
	unsupported []string
}

func (m *Module) resolveProfile(provider string, p config.NamedProfile) profileFile {
	switch provider {
	case config.ProviderClaude:
		return m.claudeProfile(p)
	case config.ProviderCodex:
		return m.codexProfile(p)
	case config.ProviderCopilot:
		return m.copilotProfile(p)
	}
	return m.opencodeProfile(p)
}

func (m *Module) claudeProfile(p config.NamedProfile) profileFile {
	share := p.Share(config.ProviderClaude)
	front := &ordered{}
	var unsupported []string
	front.set("name", p.Name)
	front.set("description", p.Description)
	if share.Model != "" {
		front.set("model", share.Model)
	}
	kept, removed := profileToolSets(p.Profile)
	servers, access := m.profileAccess(config.ProviderClaude, p.Profile)
	exposed := func(d string) bool { return d == config.PermissionAllow || d == "ask" }
	if p.Tools != nil {
		list := toolNames(kept, claudeToolNames)
		for _, s := range servers {
			if exposed(access[s]) {
				list = append(list, "mcp__"+s)
			}
		}
		front.set("tools", list)
	} else {
		list := toolNames(removed, claudeToolNames)
		for _, s := range servers {
			if !exposed(access[s]) {
				list = append(list, "mcp__"+s)
			}
		}
		if len(list) > 0 {
			front.set("disallowedTools", list)
		}
	}
	switch {
	case p.Reviewer == "auto":
		front.set("permissionMode", "auto")
	case p.Edit == config.PermissionAllow:
		front.set("permissionMode", "acceptEdits")
	case p.Edit == "ask" || p.Shell == "ask" || p.Reviewer == "user":
		front.set("permissionMode", "default")
	}
	hasShell := slices.Contains(kept, "shell")
	if p.Sandbox != "" {
		unsupported = append(unsupported, "sandbox")
	}
	if p.Shell == config.PermissionAllow {
		unsupported = append(unsupported, "shell: allow")
	}
	if p.Shell == "ask" && p.Edit == config.PermissionAllow && p.Reviewer != "auto" && hasShell {
		unsupported = append(unsupported, "shell: ask with edit: allow (acceptEdits also approves filesystem commands)")
	}
	if p.Network != nil && !*p.Network && hasShell {
		unsupported = append(unsupported, "network: false for shell commands")
	}
	for _, s := range servers {
		if access[s] == config.PermissionAllow {
			unsupported = append(unsupported, "mcp "+s+": allow")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(share.Overrides)) {
		front.set(k, share.Overrides[k])
	}
	return profileFile{front: front, unsupported: unsupported}
}

func (m *Module) codexProfile(p config.NamedProfile) profileFile {
	share := p.Share(config.ProviderCodex)
	t := map[string]any{}
	var unsupported []string
	if share.Model != "" {
		t["model"] = share.Model
	}
	sandbox := p.Sandbox
	if sandbox == "" {
		switch p.Edit {
		case config.PermissionDeny:
			sandbox = "read-only"
		case config.PermissionAllow:
			sandbox = "workspace-write"
		}
	}
	if sandbox != "" {
		t["sandbox_mode"] = sandbox
	}
	if p.Tools != nil {
		unsupported = append(unsupported, "tools")
	}
	switch p.Edit {
	case "ask":
		unsupported = append(unsupported, "edit: ask")
	case config.PermissionDeny:
		if sandbox != "read-only" {
			unsupported = append(unsupported, "edit: deny with sandbox "+sandbox)
		}
	case config.PermissionAllow:
		if sandbox == "read-only" {
			unsupported = append(unsupported, "edit: allow with sandbox read-only")
		}
	}
	switch p.Shell {
	case config.PermissionAllow:
		t["approval_policy"] = "never"
	case "ask":
		t["approval_policy"] = "on-request"
	case config.PermissionDeny:
		unsupported = append(unsupported, "shell: deny")
	}
	switch p.Reviewer {
	case "user":
		t["approvals_reviewer"] = "user"
	case "auto":
		t["approvals_reviewer"] = "auto_review"
	}
	if p.Network != nil {
		switch {
		case sandbox == "" || sandbox == "workspace-write":
			t["sandbox_workspace_write"] = map[string]any{"network_access": *p.Network}
		case sandbox == "read-only" && *p.Network:
			unsupported = append(unsupported, "network: true with sandbox read-only")
		case sandbox == "danger-full-access" && !*p.Network:
			unsupported = append(unsupported, "network: false with sandbox danger-full-access")
		}
	}
	servers, access := m.profileAccess(config.ProviderCodex, p.Profile)
	tables := map[string]any{}
	for _, s := range servers {
		switch access[s] {
		case config.PermissionAllow:
			tables[s] = map[string]any{"enabled": true, "default_tools_approval_mode": "approve"}
		case "ask":
			tables[s] = map[string]any{"enabled": true, "default_tools_approval_mode": "prompt"}
		default:
			tables[s] = map[string]any{"enabled": false}
		}
	}
	if len(tables) > 0 {
		t["mcp_servers"] = tables
	}
	for k, v := range share.Overrides {
		t[k] = v
	}
	return profileFile{table: t, unsupported: unsupported}
}

// copilotProfile is a Copilot CLI custom agent. Its tools list is written
// only when the profile takes something away: a listed tool or MCP server
// is all it can name, so servers added outside zakwas are cut with the
// rest.
func (m *Module) copilotProfile(p config.NamedProfile) profileFile {
	share := p.Share(config.ProviderCopilot)
	front := &ordered{}
	var unsupported []string
	front.set("name", p.Name)
	front.set("description", p.Description)
	if share.Model != "" {
		front.set("model", share.Model)
	}
	kept, removed := profileToolSets(p.Profile)
	servers, access := m.profileAccess(config.ProviderCopilot, p.Profile)
	exposed := func(d string) bool { return d == config.PermissionAllow || d == "ask" }
	cut := len(removed) > 0
	list := toolNames(kept, copilotToolNames)
	for _, s := range servers {
		if exposed(access[s]) {
			list = append(list, s+"/*")
		} else {
			cut = true
		}
	}
	if cut {
		front.set("tools", list)
	}
	if p.Sandbox != "" {
		unsupported = append(unsupported, "sandbox")
	}
	if p.Edit == config.PermissionAllow {
		unsupported = append(unsupported, "edit: allow")
	}
	if p.Shell == config.PermissionAllow {
		unsupported = append(unsupported, "shell: allow")
	}
	if p.Reviewer == "auto" {
		unsupported = append(unsupported, "reviewer: auto")
	}
	if p.Network != nil && !*p.Network && slices.Contains(kept, "shell") {
		unsupported = append(unsupported, "network: false for shell commands")
	}
	for _, s := range servers {
		if access[s] == config.PermissionAllow {
			unsupported = append(unsupported, "mcp "+s+": allow")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(share.Overrides)) {
		front.set(k, share.Overrides[k])
	}
	return profileFile{front: front, unsupported: unsupported}
}

func (m *Module) opencodeProfile(p config.NamedProfile) profileFile {
	share := p.Share(config.ProviderOpencode)
	front := &ordered{}
	var unsupported []string
	front.set("description", p.Description)
	front.set("mode", "primary")
	if share.Model != "" {
		front.set("model", share.Model)
	}
	perm := &ordered{}
	kept, removed := profileToolSets(p.Profile)
	for _, key := range toolNames(removed, opencodeToolKeys) {
		perm.set(key, config.PermissionDeny)
	}
	if p.Edit != "" && slices.Contains(kept, "edit") {
		perm.set("edit", p.Edit)
	}
	if (p.Shell == config.PermissionAllow || p.Shell == "ask") && slices.Contains(kept, "shell") {
		if rules, err := templates.OpencodeBashRules(&m.Agents, p.Shell); err == nil {
			bash := &ordered{}
			for _, r := range rules {
				bash.set(r.Pattern, r.Action)
			}
			perm.set("bash", bash)
		}
	}
	servers, access := m.profileAccess(config.ProviderOpencode, p.Profile)
	for _, s := range servers {
		perm.set(s+"_*", access[s])
	}
	if len(perm.keys) > 0 {
		front.set("permission", perm)
	}
	if p.Sandbox != "" {
		unsupported = append(unsupported, "sandbox")
	}
	if p.Reviewer == "auto" {
		unsupported = append(unsupported, "reviewer: auto")
	}
	if p.Network != nil && !*p.Network && slices.Contains(kept, "shell") {
		unsupported = append(unsupported, "network: false for shell commands")
	}
	for _, k := range slices.Sorted(maps.Keys(share.Overrides)) {
		front.set(k, share.Overrides[k])
	}
	return profileFile{front: front, unsupported: unsupported}
}

// profilePrompt renders the profile's prompt file for provider, empty when
// it has none.
func (m *Module) profilePrompt(provider string, p config.NamedProfile) (string, error) {
	if p.Prompt == "" {
		return "", nil
	}
	src := filepath.Clean(m.Paths.Src(p.Prompt))
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("prompt %s: %w", m.Paths.Pretty(src), pathErr(err))
	}
	text, err := templates.RenderText(filepath.Base(src), string(raw), instructionData{Home: m.Paths.Home, Vars: m.Vars, Provider: provider}, nil)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", m.Paths.Pretty(src), err)
	}
	return strings.TrimSpace(string(text)), nil
}

// renderProfile is the file zakwas writes for the profile: the provider's
// settings, then the prompt.
func (m *Module) renderProfile(provider string, p config.NamedProfile) ([]byte, error) {
	prompt, err := m.profilePrompt(provider, p)
	if err != nil {
		return nil, err
	}
	f := m.resolveProfile(provider, p)
	if provider == config.ProviderCodex {
		if prompt != "" {
			f.table["developer_instructions"] = prompt + "\n"
		}
		data, err := toml.Marshal(f.table)
		if err != nil {
			return nil, fmt.Errorf("render %s profile %s: %w", provider, p.Name, err)
		}
		return data, nil
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := errors.Join(enc.Encode(f.front), enc.Close()); err != nil {
		return nil, fmt.Errorf("render %s profile %s: %w", provider, p.Name, err)
	}
	buf.WriteString("---\n")
	if prompt != "" {
		buf.WriteString("\n" + prompt + "\n")
	}
	return buf.Bytes(), nil
}
