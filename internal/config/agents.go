package config

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Agent providers. ProviderOpencode is accepted but not converged yet, so
// configs written for it stay valid while its backend lands.
const (
	ProviderClaude   = "claude"
	ProviderCodex    = "codex"
	ProviderOpencode = "opencode"
)

// Providers lists every known provider, in the order they are planned.
var Providers = []string{ProviderClaude, ProviderCodex, ProviderOpencode}

// Agents declares agent plugin marketplaces and plugins once, for every
// provider they target.
type Agents struct {
	Providers    []string               `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode" jsonschema_description:"Providers zakwas manages: claude, codex, opencode. Marketplaces target these unless they set their own 'providers'; prune only touches these. claude and codex are converged; opencode is accepted and skipped. codex is converged only when named here or in an entry's 'providers'. A provider whose CLI is missing fails its plan; the others still converge. Default: all three."`
	Upgrade      bool                   `yaml:"upgrade" jsonschema:"default=false" jsonschema_description:"Update installed plugins to the version their marketplace offers on apply. Run 'zakwas upgrade' to refresh the marketplaces first. Default false."`
	Prune        bool                   `yaml:"prune" jsonschema:"default=false" jsonschema_description:"Remove user-scope plugins and marketplaces that are not declared here, for the managed providers. Plugins installed for a single project, marketplaces they still use, and marketplaces not declared in the provider's user settings are never touched. Removals are shown in 'zakwas plan' first. Default false."`
	Marketplaces map[string]Marketplace `yaml:"marketplaces" jsonschema_description:"Marketplaces by name: the name must match the one in the marketplace's own manifest. The value is the source (GitHub 'owner/repo', a git URL, or a local path starting with './', '../', '~/' or '/'; relative paths resolve from the directory holding zakwas.yaml), or an object with 'source' and 'providers'."`
	Plugins      []Plugin               `yaml:"plugins" jsonschema_description:"Plugins to install and enable, each 'name@marketplace' with the marketplace declared under 'marketplaces', or an object with 'id' and 'providers'. A plugin targets its marketplace's providers unless it sets its own."`
}

// Marketplace is a plugin marketplace. In YAML it is either the source
// string or a mapping.
type Marketplace struct {
	Source    string   `yaml:"source" jsonschema:"required,minLength=1" jsonschema_description:"GitHub 'owner/repo', a git URL, or a local path starting with './', '../', '~/' or '/' (relative paths resolve from the directory holding zakwas.yaml)."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode" jsonschema_description:"Providers to add this marketplace to; must be in agents.providers. Default: agents.providers."`
}

// Plugin is one plugin. In YAML it is either the "name@marketplace" string
// or a mapping.
type Plugin struct {
	ID        string   `yaml:"id" jsonschema:"required,minLength=1" jsonschema_description:"Plugin id 'name@marketplace'; the marketplace must be declared under agents.marketplaces."`
	Providers []string `yaml:"providers" jsonschema:"minItems=1,uniqueItems=true,enum=claude,enum=codex,enum=opencode" jsonschema_description:"Providers to install this plugin for; must be targeted by its marketplace. Default: the marketplace's providers."`
}

func (m *Marketplace) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&m.Source)
	}
	if err := knownKeys(n, "source", "providers"); err != nil {
		return err
	}
	type plain Marketplace
	return n.Decode((*plain)(m))
}

func (p *Plugin) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		return n.Decode(&p.ID)
	}
	if err := knownKeys(n, "id", "providers"); err != nil {
		return err
	}
	type plain Plugin
	return n.Decode((*plain)(p))
}

// knownKeys rejects unknown mapping keys, which yaml's KnownFields doesn't
// check inside custom unmarshalers.
func knownKeys(n *yaml.Node, keys ...string) error {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if !slices.Contains(keys, k.Value) {
			return fmt.Errorf("line %d: field %s not found, want one of %s", k.Line, k.Value, strings.Join(keys, ", "))
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
	return a.DefaultProviders()
}

// PluginProviders returns the providers a plugin targets.
func (a *Agents) PluginProviders(p Plugin) []string {
	if p.Providers != nil {
		return p.Providers
	}
	_, mk, _ := SplitPluginID(p.ID)
	return a.MarketplaceProviders(mk)
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
	for _, name := range a.MarketplaceNames() {
		m := a.Marketplaces[name]
		what := "agents.marketplaces." + name
		if !marketplaceName.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: name may only contain letters, digits, '.', '_' and '-'", what))
		}
		if m.Source == "" {
			errs = append(errs, fmt.Errorf("%s: source is required", what))
		}
		checkProviders(what+".providers", m.Providers, a.DefaultProviders(), "agents.providers")
	}
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
		checkProviders(what+".providers", p.Providers, a.MarketplaceProviders(mk), "the providers of marketplace "+mk)
	}
	return errs
}
