package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// agentsRootOnly are the agents keys only the main config may set: they
// decide what zakwas manages and removes for the whole machine, or configure
// a provider as a whole.
var agentsRootOnly = []string{"providers", "prune", "guard", "upgrade", "sharedInstructions", "codex", "opencode"}

// rootOnlyAgents names every agents key a layer sets that only the main
// config may, by the keys present in its YAML: an empty value still counts.
func rootOnlyAgents(top map[string]yaml.Node) []error {
	n, ok := top["agents"]
	if !ok {
		return nil
	}
	var errs []error
	for _, k := range agentsRootOnly {
		if mappingValue(&n, k) != nil {
			errs = append(errs, fmt.Errorf("agents.%s is set in a layer; only the main zakwas.yaml may set it", k))
		}
	}
	return errs
}

// agentsMerge accumulates the agents sections of the main config and its
// layers into one, remembering which file each map entry came from.
type agentsMerge struct {
	cfg  *Config
	file map[string]string
}

// newAgentsMerge copies the main config's agents section, if it has one, so
// merging never writes into the parsed main config.
func newAgentsMerge(m *Config, c *Config) *agentsMerge {
	if c.Agents == nil {
		return &agentsMerge{cfg: m}
	}
	a := *c.Agents
	a.Marketplaces = maps.Clone(a.Marketplaces)
	a.Plugins = slices.Clone(a.Plugins)
	a.Instructions = slices.Clone(a.Instructions)
	a.MCP = maps.Clone(a.MCP)
	a.Profiles = maps.Clone(a.Profiles)
	a.Skills = slices.Clone(a.Skills)
	if c.Agents.Hooks != nil {
		h := *c.Agents.Hooks
		h.Commands = slices.Clone(h.Commands)
		a.Hooks = &h
	}
	if c.Agents.Permissions != nil {
		p := *c.Agents.Permissions
		p.Allow, p.Review, p.Deny = slices.Clone(p.Allow), slices.Clone(p.Review), slices.Clone(p.Deny)
		a.Permissions = &p
	}
	a.origins = map[string][]string{}
	m.Agents = &a
	am := &agentsMerge{cfg: m, file: map[string]string{}}
	am.label("agents.plugins", len(a.Plugins), c.Path)
	am.label("agents.instructions", len(a.Instructions), c.Path)
	am.label("agents.skills", len(a.Skills), c.Path)
	if a.Hooks != nil {
		am.label("agents.hooks.commands", len(a.Hooks.Commands), c.Path)
	}
	if a.Permissions != nil {
		am.label("agents.permissions.allow", len(a.Permissions.Allow), c.Path)
		am.label("agents.permissions.review", len(a.Permissions.Review), c.Path)
		am.label("agents.permissions.deny", len(a.Permissions.Deny), c.Path)
	}
	for k := range a.Marketplaces {
		am.file["marketplaces."+k] = c.Path
	}
	for k := range a.MCP {
		am.file["mcp."+k] = c.Path
	}
	for k := range a.Profiles {
		am.file["profiles."+k] = c.Path
	}
	return am
}

func (am *agentsMerge) label(section string, n int, file string) {
	for range n {
		am.cfg.Agents.origins[section] = append(am.cfg.Agents.origins[section], file)
	}
}

// add merges the agents section of layer lc, if it sets one: lists are
// joined after the main config's, maps are merged and a key set in two files
// is an error naming both. Relative paths resolve against the layer's own
// directory. The layer's section is not validated on its own, since it may
// refer to a marketplace or MCP server of another file; the merged config
// is.
func (am *agentsMerge) add(lc *Config) []error {
	src := lc.Agents
	if src == nil {
		return nil
	}
	dst := am.cfg.Agents
	if dst == nil {
		return []error{fmt.Errorf("%s sets agents, but the main zakwas.yaml has no agents section; add one there (even an empty 'agents: {}') to manage agents", lc.Path)}
	}
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) || IsHomePath(p) {
			return p
		}
		return filepath.Join(lc.Root, p)
	}
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(src.Marketplaces)) {
		mk := src.Marketplaces[k]
		if first, dup := am.file["marketplaces."+k]; dup {
			errs = append(errs, fmt.Errorf("agents.marketplaces.%s is set in both %s and %s", k, first, lc.Path))
			continue
		}
		if IsLocalSource(mk.Source) {
			mk.Source = abs(mk.Source)
		}
		if dst.Marketplaces == nil {
			dst.Marketplaces = map[string]Marketplace{}
		}
		dst.Marketplaces[k] = mk
		am.file["marketplaces."+k] = lc.Path
	}
	for _, k := range slices.Sorted(maps.Keys(src.MCP)) {
		if first, dup := am.file["mcp."+k]; dup {
			errs = append(errs, fmt.Errorf("agents.mcp.%s is set in both %s and %s", k, first, lc.Path))
			continue
		}
		if dst.MCP == nil {
			dst.MCP = map[string]MCPServer{}
		}
		dst.MCP[k] = src.MCP[k]
		am.file["mcp."+k] = lc.Path
	}
	for _, k := range slices.Sorted(maps.Keys(src.Profiles)) {
		if first, dup := am.file["profiles."+k]; dup {
			errs = append(errs, fmt.Errorf("agents.profiles.%s is set in both %s and %s", k, first, lc.Path))
			continue
		}
		p := src.Profiles[k]
		p.Prompt = abs(p.Prompt)
		if dst.Profiles == nil {
			dst.Profiles = map[string]Profile{}
		}
		dst.Profiles[k] = p
		am.file["profiles."+k] = lc.Path
	}
	dst.Plugins = append(dst.Plugins, src.Plugins...)
	am.label("agents.plugins", len(src.Plugins), lc.Path)
	for _, f := range src.Instructions {
		dst.Instructions = append(dst.Instructions, abs(f))
	}
	am.label("agents.instructions", len(src.Instructions), lc.Path)
	for _, s := range src.Skills {
		if s.Git == "" {
			s.Path = abs(s.Path)
		}
		dst.Skills = append(dst.Skills, s)
	}
	am.label("agents.skills", len(src.Skills), lc.Path)
	if src.Hooks != nil {
		if dst.Hooks == nil {
			dst.Hooks = &AgentHooks{}
		}
		dst.Hooks.Klaudiush = dst.Hooks.Klaudiush || src.Hooks.Klaudiush
		dst.Hooks.Commands = append(dst.Hooks.Commands, src.Hooks.Commands...)
		am.label("agents.hooks.commands", len(src.Hooks.Commands), lc.Path)
	}
	if p := src.Permissions; p != nil {
		if dst.Permissions == nil {
			dst.Permissions = &AgentPermissions{}
		}
		dst.Permissions.Allow = append(dst.Permissions.Allow, p.Allow...)
		dst.Permissions.Review = append(dst.Permissions.Review, p.Review...)
		dst.Permissions.Deny = append(dst.Permissions.Deny, p.Deny...)
		am.label("agents.permissions.allow", len(p.Allow), lc.Path)
		am.label("agents.permissions.review", len(p.Review), lc.Path)
		am.label("agents.permissions.deny", len(p.Deny), lc.Path)
	}
	return errs
}

// at names entry i of an agents list: by position, and by the file it came
// from when layers are merged, so two entries in conflict name both files.
func (a *Agents) at(section string, i int) string {
	if files := a.origins[section]; i < len(files) {
		return fmt.Sprintf("%s: %s[%d]", files[i], section, i)
	}
	return fmt.Sprintf("%s[%d]", section, i)
}

// IsLocalSource reports whether a marketplace source is a filesystem path.
func IsLocalSource(src string) bool {
	return src == "." || src == ".." || IsHomePath(src) ||
		strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") ||
		strings.HasPrefix(src, "."+string(filepath.Separator)) || strings.HasPrefix(src, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(src)
}
