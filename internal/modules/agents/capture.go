package agents

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

// Kinds of what Capture finds.
const (
	KindMarketplace = "marketplace"
	KindPlugin      = "plugin"
	KindMCP         = "mcp"
	KindPermission  = "permission"
)

var kindOrder = []string{KindMarketplace, KindPlugin, KindMCP, KindPermission}

// CapturedServer is the part of an MCP server that a proposal can declare.
type CapturedServer struct {
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	URL            string   `json:"url,omitempty"`
	EnvVars        []string `json:"env_vars,omitempty"`
	BearerTokenEnv string   `json:"bearer_token_env,omitempty"`
}

func (s CapturedServer) mcp() config.MCPServer {
	return config.MCPServer{Command: s.Command, Args: s.Args, URL: s.URL, EnvVars: s.EnvVars, BearerTokenEnv: s.BearerTokenEnv}
}

// Summary is the server in one line, without anything that could be a
// secret.
func (s CapturedServer) Summary() string {
	if s.URL != "" {
		return "http " + s.URL
	}
	return strings.Join(append([]string{"stdio", s.Command}, s.Args...), " ")
}

// Candidate is something one provider has that zakwas.yaml doesn't declare.
// Name is the marketplace name, plugin id, server name or command prefix.
// A candidate with a Skip reason can't be turned into a declaration.
type Candidate struct {
	Provider      string          `json:"provider"`
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	Source        string          `json:"source,omitempty"`
	Decision      string          `json:"decision,omitempty"`
	Justification string          `json:"justification,omitempty"`
	Server        *CapturedServer `json:"server,omitempty"`
	Found         string          `json:"found_in,omitempty"`
	Skip          string          `json:"skipped,omitempty"`
}

// sentinelMarketplaces are the marketplace names Claude gives plugins that
// come from no marketplace: --plugin-dir, .claude/skills, claude.ai and
// the ones built in.
var sentinelMarketplaces = []string{"inline", "skills-dir", "synced", "builtin"}

// Capture lists what the managed providers have that zakwas.yaml doesn't
// declare, without changing anything: user-scope plugins and marketplaces,
// MCP servers, Claude's allow rules and the commands approved in Codex's
// default.rules. Plugins that come from no marketplace, project and local
// installs, and what zakwas added itself are never listed. opencode has
// no plugin commands, and gets servers and rules through its templates.
//
// A provider that can't be read fails alone: the others' candidates are
// returned along with the error.
func (m *Module) Capture(ctx context.Context) ([]Candidate, error) {
	var out []Candidate
	var errs []error
	for _, p := range []string{config.ProviderClaude, config.ProviderCodex} {
		if !m.Agents.Manages(p) || p == config.ProviderCodex && !m.Agents.Names(p) {
			continue
		}
		var found []Candidate
		var err error
		if p == config.ProviderClaude {
			found, err = m.captureClaude(ctx)
		} else {
			found, err = m.captureCodex(ctx)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, found...)
	}
	slices.SortStableFunc(out, func(a, b Candidate) int {
		return cmp.Or(
			cmp.Compare(slices.Index(config.Providers, a.Provider), slices.Index(config.Providers, b.Provider)),
			cmp.Compare(slices.Index(kindOrder, a.Kind), slices.Index(kindOrder, b.Kind)),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Decision, b.Decision),
		)
	})
	return out, errors.Join(errs...)
}

// declared is what zakwas.yaml declares for one provider.
type declared struct {
	marketplaces map[string]bool
	plugins      map[string]bool
}

func (m *Module) declaredFor(provider string) declared {
	d := declared{marketplaces: map[string]bool{}, plugins: map[string]bool{}}
	for name := range m.Agents.Marketplaces {
		if slices.Contains(m.Agents.MarketplaceProviders(name), provider) {
			d.marketplaces[name] = true
		}
	}
	for _, p := range m.Agents.Plugins {
		if slices.Contains(m.Agents.PluginProviders(p), provider) {
			d.plugins[p.ID] = true
		}
	}
	return d
}

// pluginCandidate rates one installed user-scope plugin. capturable names
// the marketplaces that this scan found a declaration for.
func (m *Module) pluginCandidate(provider, id string, enabled bool, d declared, capturable map[string]bool) Candidate {
	c := Candidate{Provider: provider, Kind: KindPlugin, Name: id}
	_, mk, _ := config.SplitPluginID(id)
	switch {
	case !enabled:
		c.Skip = "installed but disabled, and declaring it would enable it"
	case !d.marketplaces[mk] && !capturable[mk]:
		c.Skip = fmt.Sprintf("its marketplace %q is neither declared for %s nor one that capture can declare", mk, provider)
	}
	return c
}

func (m *Module) marketplaceCandidate(provider, name, source string) Candidate {
	c := Candidate{Provider: provider, Kind: KindMarketplace, Name: name, Source: source}
	switch _, named := m.Agents.Marketplaces[name]; {
	case named:
		c.Skip = "declared in zakwas.yaml for other providers only; add this provider to its providers by hand"
	case source == "":
		c.Skip = "no source to declare"
	}
	return c
}

func (m *Module) captureClaude(ctx context.Context) ([]Candidate, error) {
	c := m.claudeBackend()
	if !c.runner.Installed("claude") {
		return nil, errors.New("claude: Claude Code is not installed (no claude on PATH); install it, or remove claude from agents.providers")
	}
	st, err := c.state(ctx)
	if err != nil {
		return nil, err
	}
	userDeclared, err := c.userDeclared()
	if err != nil {
		return nil, err
	}
	d := m.declaredFor(config.ProviderClaude)
	var out []Candidate
	capturable := map[string]bool{}
	for _, name := range sortedKeys(st.marketplaces) {
		if _, user := userDeclared[name]; !user || d.marketplaces[name] || slices.Contains(sentinelMarketplaces, name) {
			continue
		}
		cand := m.marketplaceCandidate(config.ProviderClaude, name, st.marketplaces[name].source())
		capturable[name] = cand.Skip == ""
		out = append(out, cand)
	}
	installed := slices.Clone(st.installed)
	slices.SortFunc(installed, func(a, b claudeInstalled) int { return cmp.Compare(a.ID, b.ID) })
	for _, p := range installed {
		_, mk, ok := config.SplitPluginID(p.ID)
		if _, configured := st.marketplaces[mk]; !ok || !configured || p.Scope != scopeUser || slices.Contains(sentinelMarketplaces, mk) || d.plugins[p.ID] {
			continue
		}
		out = append(out, m.pluginCandidate(config.ProviderClaude, p.ID, p.Enabled, d, capturable))
	}
	servers, err := m.captureMCP(config.ProviderClaude, m.claudeMCP())
	if err != nil {
		return nil, err
	}
	rules, err := m.captureClaudeRules()
	if err != nil {
		return nil, err
	}
	return append(append(out, servers...), rules...), nil
}

func (m *Module) captureCodex(ctx context.Context) ([]Candidate, error) {
	c := m.codexBackend()
	if !c.runner.Installed("codex") {
		return nil, errors.New("codex: Codex is not installed (no codex on PATH); install it, or remove codex from agents.providers")
	}
	st, err := c.state(ctx)
	if err != nil {
		return nil, err
	}
	d := m.declaredFor(config.ProviderCodex)
	var out []Candidate
	capturable := map[string]bool{}
	for _, name := range sortedKeys(st.configured) {
		if d.marketplaces[name] {
			continue
		}
		cand := m.marketplaceCandidate(config.ProviderCodex, name, st.configured[name].source())
		capturable[name] = cand.Skip == ""
		out = append(out, cand)
	}
	installed := slices.Clone(st.installed)
	slices.SortFunc(installed, func(a, b codexPlugin) int { return cmp.Compare(a.PluginID, b.PluginID) })
	for _, p := range installed {
		_, mk, ok := config.SplitPluginID(p.PluginID)
		if _, configured := st.configured[mk]; !ok || !configured || d.plugins[p.PluginID] {
			continue
		}
		out = append(out, m.pluginCandidate(config.ProviderCodex, p.PluginID, p.Enabled, d, capturable))
	}
	servers, err := m.captureMCP(config.ProviderCodex, m.codexMCP())
	if err != nil {
		return nil, err
	}
	rules, err := m.captureCodexRules()
	if err != nil {
		return nil, err
	}
	return append(append(out, servers...), rules...), nil
}

// captureMCP lists the servers in the provider's config that zakwas.yaml
// doesn't declare and zakwas didn't add. A server becomes a declaration only
// when the declaration renders back to exactly the entry that is there.
func (m *Module) captureMCP(provider string, store mcpStore) ([]Candidate, error) {
	have, err := store.load(store.path())
	if err != nil {
		return nil, fmt.Errorf("%s: mcp: %w", provider, err)
	}
	st, err := m.loadMCPState()
	if err != nil {
		return nil, fmt.Errorf("%s: mcp: %w", provider, err)
	}
	owned := st.owned(provider, store.path())
	var out []Candidate
	for _, name := range sortedKeys(have) {
		if _, declared := m.Agents.MCP[name]; declared {
			continue
		}
		if _, added := owned[name]; added {
			continue
		}
		c := Candidate{Provider: provider, Kind: KindMCP, Name: name, Found: m.Paths.Pretty(store.path())}
		server, reason := capturedServer(provider, have[name].value)
		if reason == "" {
			reason = roundTrip(store, name, server, have[name])
		}
		if reason != "" {
			c.Skip = reason
		} else {
			c.Server = &server
		}
		out = append(out, c)
	}
	return out, nil
}

// roundTrip returns why server, rendered the way zakwas.yaml would have it
// rendered, isn't the entry that is there, or "".
func roundTrip(store mcpStore, name string, server CapturedServer, have mcpEntry) string {
	want, err := store.render(name, server.mcp())
	if err != nil {
		return "can't be declared: " + err.Error()
	}
	if want.canon == have.canon {
		return ""
	}
	return fmt.Sprintf("has settings capture can't declare (%s), which may hold literal values; add it to zakwas.yaml by hand",
		strings.Join(differingKeys(have.value, want.value), ", "))
}

var (
	bearerEnv = regexp.MustCompile(`^Bearer \$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	envRef    = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
)

// capturedServer reads the fields zakwas can declare out of a provider's
// normalized entry; roundTrip then proves nothing else is in it. The reason
// is non-empty for a URL that may carry a key.
func capturedServer(provider string, v map[string]any) (CapturedServer, string) {
	var s CapturedServer
	if raw, _ := v["url"].(string); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return s, "its URL has credentials, a query or a fragment, which could hold a key; add it to zakwas.yaml by hand"
		}
		s.URL = raw
		if provider == config.ProviderCodex {
			s.BearerTokenEnv, _ = v["bearer_token_env_var"].(string)
		} else if headers, ok := v["headers"].(map[string]any); ok {
			auth, _ := headers["Authorization"].(string)
			if g := bearerEnv.FindStringSubmatch(auth); g != nil {
				s.BearerTokenEnv = g[1]
			}
		}
		return s, ""
	}
	s.Command, _ = v["command"].(string)
	s.Args = mcpStrings(v["args"])
	if provider == config.ProviderCodex {
		s.EnvVars = mcpStrings(v["env_vars"])
		return s, ""
	}
	env, _ := v["env"].(map[string]any)
	for _, key := range sortedKeys(env) {
		if val, _ := env[key].(string); envRef.FindStringSubmatch(val) != nil && envRef.FindStringSubmatch(val)[1] == key {
			s.EnvVars = append(s.EnvVars, key)
		}
	}
	return s, ""
}

func mcpStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// captureClaudeRules lists the allow rules of Claude's user settings that
// zakwas.yaml doesn't declare and zakwas didn't add.
func (m *Module) captureClaudeRules() ([]Candidate, error) {
	path := m.claudeSettingsPath()
	data, err := readIfExists(path)
	if err != nil {
		return nil, fmt.Errorf("claude: permissions: %w", err)
	}
	if data == nil {
		return nil, nil
	}
	o, err := parseObject(path, data)
	if err != nil {
		return nil, fmt.Errorf("claude: permissions: %w", err)
	}
	var perms object
	if raw, ok := o.get("permissions"); ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return nil, fmt.Errorf("claude: %s permissions: %w", path, err)
		}
	}
	var allow []string
	if raw, ok := perms.get("allow"); ok {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return nil, fmt.Errorf("claude: %s permissions.allow: %w", path, err)
		}
	}
	st, err := m.loadPermissionsState()
	if err != nil {
		return nil, fmt.Errorf("claude: permissions: %w", err)
	}
	skip := slices.Concat(ownedClaudeRules(st, path).Allow, m.desiredClaudeRules().Allow)
	known := m.declaredCommands(config.ProviderClaude)
	var out []Candidate
	seen := map[string]bool{}
	for _, rule := range allow {
		if slices.Contains(skip, rule) || seen[rule] {
			continue
		}
		seen[rule] = true
		cmd, reason := claudeBashPrefix(rule)
		if reason == "" && known[cmd] {
			continue
		}
		c := Candidate{Provider: config.ProviderClaude, Kind: KindPermission, Name: cmd, Decision: config.PermissionAllow, Found: m.Paths.Pretty(path)}
		if reason != "" {
			c.Name, c.Skip = rule, reason
		}
		out = append(out, c)
	}
	return out, nil
}

// declaredCommands are the command prefixes zakwas.yaml gives a decision
// for one provider.
func (m *Module) declaredCommands(provider string) map[string]bool {
	out := map[string]bool{}
	for _, p := range m.Agents.PermissionsFor(provider) {
		out[p.Command()] = true
	}
	return out
}

// claudeBashPrefix reads the command prefix of a Claude Bash rule, in the
// "git status:*" or "git status *" spelling, or says why it isn't one.
func claudeBashPrefix(rule string) (string, string) {
	inner, ok := strings.CutPrefix(rule, "Bash(")
	if !ok || !strings.HasSuffix(inner, ")") {
		if rule == "Bash" {
			return "", "allows every command, which zakwas doesn't declare"
		}
		return "", "not a Bash command rule; zakwas declares only command prefixes"
	}
	inner = strings.TrimSuffix(inner, ")")
	var prefix string
	switch {
	case inner == "*":
	case strings.HasSuffix(inner, ":*"):
		prefix = strings.TrimSuffix(inner, ":*")
	case strings.HasSuffix(inner, " *"):
		prefix = strings.TrimSuffix(inner, " *")
	default:
		return "", "matches one exact command, and a declared prefix would allow more"
	}
	words := strings.Fields(prefix)
	if len(words) == 0 {
		return "", "allows every command, which zakwas doesn't declare"
	}
	return strings.Join(words, " "), ""
}

var codexRuleDecisions = map[string]string{
	"allow":     config.PermissionAllow,
	"prompt":    config.PermissionReview,
	"forbidden": config.PermissionDeny,
}

// captureCodexRules lists the prefix rules of Codex's default.rules, where
// approving a command in Codex saves it, that zakwas.yaml doesn't declare.
// The rules file zakwas writes is another file.
func (m *Module) captureCodexRules() ([]Candidate, error) {
	path := filepath.Join(m.codexBackend().home, "rules", "default.rules")
	data, err := readRegular(path)
	if err != nil {
		return nil, fmt.Errorf("codex: %s: %w", m.Paths.Pretty(path), err)
	}
	known := m.declaredCommands(config.ProviderCodex)
	var out []Candidate
	seen := map[string]bool{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "prefix_rule(") {
			continue
		}
		words, decision, justification, reason := parsePrefixRule(line)
		c := Candidate{
			Provider: config.ProviderCodex, Kind: KindPermission, Name: strings.Join(words, " "),
			Decision: decision, Justification: justification, Found: m.Paths.Pretty(path),
		}
		if reason != "" {
			c.Name, c.Skip = line, reason
		}
		key := c.Decision + "\x00" + c.Name
		if seen[key] || reason == "" && known[c.Name] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out, nil
}

var (
	ruleDecision      = regexp.MustCompile(`decision\s*=\s*"([a-z]+)"`)
	ruleJustification = regexp.MustCompile(`justification\s*=\s*"((?:[^"\\]|\\.)*)"`)
	rulePattern       = regexp.MustCompile(`pattern\s*=\s*\[((?:[^\]"\[]|"(?:[^"\\]|\\.)*")*)\]`)
	ruleString        = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	ruleEscape        = strings.NewReplacer(`\\`, `\`, `\"`, `"`)
)

// parsePrefixRule reads a one-line prefix_rule: the words of its pattern,
// its decision as a zakwas decision, and its justification. A rule whose
// pattern has alternatives, or whose decision zakwas doesn't know, comes
// back with the reason it can't be declared.
func parsePrefixRule(line string) (words []string, decision, justification, reason string) {
	m := rulePattern.FindStringSubmatch(line)
	if m == nil {
		return nil, "", "", "its pattern has alternatives or spans lines, which capture can't declare"
	}
	for _, s := range ruleString.FindAllStringSubmatch(m[1], -1) {
		words = append(words, ruleEscape.Replace(s[1]))
	}
	if len(words) == 0 {
		return nil, "", "", "its pattern is empty"
	}
	d := ruleDecision.FindStringSubmatch(line)
	if d == nil {
		decision = config.PermissionAllow
	} else if decision = codexRuleDecisions[d[1]]; decision == "" {
		return words, "", "", fmt.Sprintf("its decision %q is not one zakwas declares", d[1])
	}
	if j := ruleJustification.FindStringSubmatch(line); j != nil {
		justification = ruleEscape.Replace(j[1])
	}
	return words, decision, justification, ""
}
