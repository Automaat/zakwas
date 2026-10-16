package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
)

// Gemini CLI has no plugin marketplaces and rewrites its own settings.json,
// whose memory tool also writes GEMINI.md, so zakwas owns neither. It writes
// one extension, linked with `gemini extensions link`, that carries the MCP
// servers, the context file, the command hooks and the subagents, plus the
// policy file ${GEMINI_CLI_HOME:-~}/.gemini/policies/zakwas.toml: Gemini
// ignores allow rules from extensions and ranks extension rules below user
// ones, so every permission rule lives in the user file to keep deny over
// ask over allow. Skills are linked into ~/.agents/skills like Codex's.
const geminiExtension = "zakwas"

// GeminiStatePath is where zakwas records the hashes of the extension and
// policy files it wrote for Gemini CLI under home.
func GeminiStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "gemini-files.json")
}

func (m *Module) geminiStatePath() string { return GeminiStatePath(m.Paths.Home) }

// geminiDir is Gemini CLI's config dir: GEMINI_CLI_HOME moves the .gemini
// directory, and the .agents one, under it.
func (m *Module) geminiDir() string { return filepath.Join(m.geminiBase(), ".gemini") }

func (m *Module) geminiBase() string {
	if m.GeminiHome != "" {
		return m.GeminiHome
	}
	return m.Paths.Home
}

// geminiExtDir is where the extension zakwas renders lives, apart from
// ~/.gemini/extensions, where gemini keeps its own record of a linked one.
func (m *Module) geminiExtDir() string { return filepath.Join(m.geminiDir(), geminiExtension) }

func (m *Module) geminiPolicyPath() string {
	return filepath.Join(m.geminiDir(), "policies", "zakwas.toml")
}

func (m *Module) geminiInstaller() *install.Installer {
	if m.gemini == nil {
		m.gemini = &install.Installer{Paths: m.Paths, StatePath: m.geminiStatePath(), Immutable: m.Immutable}
	}
	return m.gemini
}

func (m *Module) geminiCmd(args ...string) runner.Cmd {
	c := runner.Cmd{Name: "gemini", Args: args, Dir: "/"}
	if m.GeminiHome != "" {
		c.Env = []string{"GEMINI_CLI_HOME=" + m.GeminiHome}
	}
	return c
}

func geminiCmdAt(base string, args ...string) runner.Cmd {
	return runner.Cmd{Name: "gemini", Args: args, Dir: "/", Env: []string{"GEMINI_CLI_HOME=" + base}}
}

// geminiNativeEvent maps a shared hook event to Gemini's: AfterAgent fires
// once per turn, after the final response.
func geminiNativeEvent(event string) string {
	switch event {
	case "beforeTool":
		return "BeforeTool"
	case "afterTool":
		return "AfterTool"
	case "sessionStart":
		return "SessionStart"
	case "stop":
		return "AfterAgent"
	}
	return ""
}

type geminiServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	HTTPURL string            `json:"httpUrl,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type geminiManifest struct {
	Name            string                  `json:"name"`
	Version         string                  `json:"version"`
	Description     string                  `json:"description"`
	ContextFileName string                  `json:"contextFileName,omitempty"`
	MCPServers      map[string]geminiServer `json:"mcpServers,omitempty"`
}

// geminiServerGaps names what keeps a server out of Gemini CLI. Gemini
// expands $NAME and ${NAME} anywhere in gemini-extension.json when it loads
// the extension, so a literal dollar sign in a server's command, args, url,
// env or headers would be silently replaced; the variables zakwas writes
// itself are what that expansion is for.
func geminiServerGaps(name string, s config.MCPServer) []string {
	var gaps []string
	if strings.Contains(name, "_") {
		gaps = append(gaps, "its name has an underscore, which Gemini's policy rules misread")
	}
	literal := slices.Concat([]string{s.Command, s.URL}, s.Args, slices.Collect(maps.Values(s.Env)), slices.Collect(maps.Values(s.Headers)))
	if slices.ContainsFunc(literal, func(v string) bool { return strings.Contains(v, "$") }) {
		gaps = append(gaps, "Gemini would expand the $ in its command, args, url, env or headers")
	}
	return gaps
}

func geminiServerEntry(s config.MCPServer) geminiServer {
	if !s.IsHTTP() {
		env := maps.Clone(s.Env)
		if env == nil {
			env = map[string]string{}
		}
		for _, name := range s.EnvVars {
			env[name] = "$" + name
		}
		return geminiServer{Command: s.Command, Args: s.Args, Env: env}
	}
	headers := maps.Clone(s.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	for h, name := range s.HeaderEnv {
		headers[h] = "$" + name
	}
	if s.BearerTokenEnv != "" {
		headers["Authorization"] = "Bearer $" + s.BearerTokenEnv
	}
	return geminiServer{HTTPURL: s.URL, Headers: headers}
}

// geminiServers are the servers the extension registers, and per left-out
// server what keeps it out.
func (m *Module) geminiServers() (servers map[string]geminiServer, skipped map[string][]string) {
	servers, skipped = map[string]geminiServer{}, map[string][]string{}
	for _, s := range m.Agents.MCPFor(config.ProviderGemini) {
		if gaps := geminiServerGaps(s.Name, s.MCPServer); len(gaps) > 0 {
			skipped[s.Name] = gaps
			continue
		}
		servers[s.Name] = geminiServerEntry(s.MCPServer)
	}
	return servers, skipped
}

// geminiContext reports whether the extension carries the instructions.
func (m *Module) geminiContext() bool {
	return len(m.Agents.Instructions) > 0 && m.Agents.Names(config.ProviderGemini)
}

// geminiNeedsExtension reports whether anything goes in the extension; skills
// and permissions don't, so a config with only those writes no extension.
func (m *Module) geminiNeedsExtension() bool {
	servers, _ := m.geminiServers()
	hooks, _ := m.renderGeminiHooks()
	return len(servers) > 0 || hooks != nil || m.geminiContext() || len(m.Agents.ProfilesFor(config.ProviderGemini)) > 0
}

func (m *Module) renderGeminiManifest() ([]byte, error) {
	servers, _ := m.geminiServers()
	manifest := geminiManifest{
		Name: geminiExtension, Version: "1.0.0",
		Description: "Managed by zakwas from the agents section of zakwas.yaml; edit it there.",
		MCPServers:  servers,
	}
	if m.geminiContext() {
		manifest.ContextFileName = "GEMINI.md"
	}
	return indentJSON(manifest)
}

func indentJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// renderGeminiHooks is the extension's hooks/hooks.json, nil without hooks.
func (m *Module) renderGeminiHooks() ([]byte, error) {
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type group struct {
		Hooks []hook `json:"hooks"`
	}
	events := map[string][]group{}
	for _, h := range m.desiredCommandHooks(config.ProviderGemini) {
		native := geminiNativeEvent(h.Event)
		events[native] = append(events[native], group{Hooks: []hook{{Type: "command", Command: h.Command}}})
	}
	if len(events) == 0 {
		return nil, nil
	}
	return indentJSON(map[string]any{"hooks": events})
}

// Priorities of the rules in zakwas.toml, 0 to 999 within the user tier: the
// highest wins, so deny beats ask beats allow, and the rules of a profile
// sit below all of them, where the allow rules still let their commands
// through.
const (
	geminiGuardPriority   = 990
	geminiDenyPriority    = 900
	geminiReviewPriority  = 700
	geminiAllowPriority   = 500
	geminiProfilePriority = 400
	// A profile's deny beats the allow rules of agents.permissions.
	geminiProfileDenyPriority = 800
)

// geminiRule is one [[rule]] of a Gemini policy file.
type geminiRule struct {
	ToolName      any    `toml:"toolName,omitempty"`
	Subagent      string `toml:"subagent,omitempty"`
	MCPName       string `toml:"mcpName,omitempty"`
	CommandPrefix string `toml:"commandPrefix,omitempty"`
	CommandRegex  string `toml:"commandRegex,omitempty"`
	Decision      string `toml:"decision"`
	Priority      int    `toml:"priority"`
	DenyMessage   string `toml:"denyMessage,omitempty"`
}

var geminiDecisions = map[string]string{
	config.PermissionAllow:  "allow",
	config.PermissionReview: "ask_user",
	config.PermissionDeny:   "deny",
}

var geminiPriorities = map[string]int{
	config.PermissionAllow:  geminiAllowPriority,
	config.PermissionReview: geminiReviewPriority,
	config.PermissionDeny:   geminiDenyPriority,
}

// The pieces of geminiGuardRegex. Gemini tests the regex against the
// JSON-encoded call, `"command":"<command>"`, where a quote is `\"`, a tab
// is `\t` and a newline `\n`, and rejects a quantified group as unsafe, so
// the quoted values are nested optional groups and none is repeated.
const (
	geminiGuardQuotedValues = 6
	guardLead               = `[ ]*(?:|\\t[ ]*(?:|\\t[ ]*))`
	guardBlank              = `(?:[ ]|\\t)[ ]*(?:|\\t[ ]*)`
	guardOneBlank           = `(?:[ ]|\\t)`
	guardWordEnd            = `(?:[ ;&|)<>"]|\\[tn])`
	guardAfterBlank         = `(?:[-;&|<>$#"` + "`" + `]|\\\\|\\"[-$` + "`" + `]|'[-$` + "`" + `]|[0-9]+[<>])`
	guardQuotedEnd          = `(?:"|[;&|)<>]|\\n|` + guardBlank + guardAfterBlank + `)`
	guardEscapedQuote       = `\\\\\\"`
	guardEscapedApostrophe  = `\\\\'`
	guardDoubleQuoted       = `\\"[^"]*(?:|` + guardEscapedQuote + `[^"]*(?:|` + guardEscapedQuote + `[^"]*))\\"`
	guardSingleQuoted       = `'[^']*(?:|` + guardEscapedApostrophe + `[^']*)'`
	guardQuoted             = `(?:` + guardDoubleQuoted + `|` + guardSingleQuoted + `|` + guardEscapedQuote + `|` + guardEscapedApostrophe + `)`
	guardFlagStart          = `(?:-|\\\\|[<>&${` + "`" + `]|[0-9]+[<>]|\\"[-$][^"]*\\"|'[-$][^']*')`
)

// geminiGuardRegex matches a guarded command that starts the shell command
// as the agent wrote it: the binary bare, by path or quoted, in any case,
// then the guarded word as the first word after the binary or after flags
// and their values, as in `zakwas -c "my dir/x.yaml" apply`. A word before
// the flags, as in `zakwas plan apply`, makes that word the command, so it
// does not match. A quoted guarded word after flags matches unless a plain
// word follows it, as in `zakwas -m "apply" plan`; the real command takes
// no word there, only flags.
func geminiGuardRegex(words []string) string {
	name := caselessWord(words[0])
	word := regexp.QuoteMeta(strings.Join(words[1:], " "))
	quoted := `(?:\\"` + word + `\\"|'` + word + `')`
	prefix := `(?:|[^ ]*/|\\"[^"]*/\\"|'[^']*/')`
	binary := `(?:` + prefix + `(?:` + name + `|\\"` + name + `\\"|'` + name + `')|\\"[^"]*/` + name + `\\"|'[^']*/` + name + `')`
	rest := `[^"']*`
	for range geminiGuardQuotedValues {
		rest = `[^"']*(?:|` + guardQuoted + rest + `)`
	}
	first := `(?:` + word + guardWordEnd + `|` + quoted + guardWordEnd + `)`
	later := `(?:` + word + guardWordEnd + `|` + quoted + guardQuotedEnd + `)`
	return guardLead + binary + guardBlank + `(?:` + first + `|` + guardFlagStart + rest + guardOneBlank + later + `)`
}

func caselessWord(word string) string {
	var b strings.Builder
	for _, r := range word {
		upper, lower := unicode.ToUpper(r), unicode.ToLower(r)
		if upper == lower {
			b.WriteString(regexp.QuoteMeta(string(r)))
			continue
		}
		b.WriteString("[" + string(upper) + string(lower) + "]")
	}
	return b.String()
}

// geminiRules are the rules of zakwas.toml: the guard's, agents.permissions
// and the decisions of the profiles.
func (m *Module) geminiRules() []geminiRule {
	var rules []geminiRule
	if m.Guard {
		for _, g := range m.Agents.GuardPermissions(config.ProviderGemini) {
			rules = append(rules, geminiRule{
				ToolName: "run_shell_command", CommandRegex: geminiGuardRegex(g.Words),
				Decision: "deny", Priority: geminiGuardPriority, DenyMessage: g.Justification,
			})
		}
	}
	for _, p := range m.Agents.PermissionsFor(config.ProviderGemini) {
		r := geminiRule{
			ToolName: "run_shell_command", CommandPrefix: p.Command(),
			Decision: geminiDecisions[p.Decision], Priority: geminiPriorities[p.Decision],
		}
		if p.Decision == config.PermissionDeny {
			r.DenyMessage = p.Justification
		}
		rules = append(rules, r)
	}
	for _, prof := range m.Agents.ProfilesFor(config.ProviderGemini) {
		rules = append(rules, m.geminiProfile(prof).rules...)
	}
	return rules
}

func (m *Module) renderGeminiPolicy() ([]byte, error) {
	rules := m.geminiRules()
	if len(rules) == 0 {
		return nil, nil
	}
	return toml.Marshal(struct {
		Rule []geminiRule `toml:"rule"`
	}{rules})
}

// geminiFile is one file the extension or the policy dir gets.
type geminiFile struct {
	path    string
	content []byte
	detail  string
}

func (m *Module) geminiFiles() ([]geminiFile, error) {
	var files []geminiFile
	if m.geminiNeedsExtension() {
		manifest, err := m.renderGeminiManifest()
		if err != nil {
			return nil, err
		}
		files = append(files, geminiFile{path: filepath.Join(m.geminiExtDir(), "gemini-extension.json"), content: manifest, detail: "gemini extension manifest"})
		hooks, err := m.renderGeminiHooks()
		if err != nil {
			return nil, err
		}
		if hooks != nil {
			files = append(files, geminiFile{path: filepath.Join(m.geminiExtDir(), "hooks", "hooks.json"), content: hooks, detail: "gemini command hooks"})
		}
	}
	policy, err := m.renderGeminiPolicy()
	if err != nil {
		return nil, err
	}
	if policy != nil {
		files = append(files, geminiFile{path: m.geminiPolicyPath(), content: policy, detail: "gemini policy rules"})
	}
	return files, nil
}

// geminiInstall is what gemini records of a linked extension.
type geminiInstall struct {
	Source string `json:"source"`
	Type   string `json:"type"`
}

func (m *Module) geminiInstallPath() string { return geminiInstallPathAt(m.geminiBase()) }

func geminiInstallPathAt(base string) string {
	return filepath.Join(base, ".gemini", "extensions", geminiExtension, ".gemini-extension-install.json")
}

// geminiLinked reads gemini's record of the zakwas extension. Reading it
// instead of running `gemini extensions list` keeps plan from creating
// gemini's project files.
func (m *Module) geminiLinked() (*geminiInstall, error) {
	return m.geminiLinkedAt(m.geminiBase())
}

func (m *Module) geminiLinkedAt(base string) (*geminiInstall, error) {
	path := geminiInstallPathAt(base)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in geminiInstall
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("%s: %w", m.Paths.Pretty(path), err)
	}
	return &in, nil
}

// geminiStaleBases are the GEMINI_CLI_HOME values an earlier apply wrote
// the extension manifest under, other than the current one, read from the
// recorded files.
func (m *Module) geminiStaleBases(tracked []string) []string {
	current := samePath(filepath.Join(m.geminiExtDir(), "gemini-extension.json"))
	var bases []string
	for _, dst := range tracked {
		ext := filepath.Dir(dst)
		dot := filepath.Dir(ext)
		if filepath.Base(dst) != "gemini-extension.json" || filepath.Base(ext) != geminiExtension || filepath.Base(dot) != ".gemini" || samePath(dst) == current {
			continue
		}
		if base := filepath.Dir(dot); !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	return bases
}

// planGeminiStaleUnlinks uninstalls the extension zakwas linked under a
// GEMINI_CLI_HOME it no longer uses, with that home in the environment, since
// gemini keeps its record per home and would warn about the missing manifest
// on every start. The old manifest's removal comes after, as gemini needs
// it. A record that can't be read, isn't a link zakwas made or has lost its
// manifest is left alone. keep holds the manifests that must stay until gemini
// is installed.
func (m *Module) planGeminiStaleUnlinks(tracked []string) (changes []engine.Change, keep map[string]bool) {
	keep = map[string]bool{}
	for _, base := range m.geminiStaleBases(tracked) {
		have, err := m.geminiLinkedAt(base)
		dir := filepath.Join(base, ".gemini", geminiExtension)
		if err != nil || have == nil || have.Type != "link" || sameFile(have.Source) != sameFile(dir) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "gemini-extension.json")); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if !m.Runner.Installed("gemini") {
			keep[samePath(filepath.Join(dir, "gemini-extension.json"))] = true
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "gemini extension " + geminiExtension + " in " + m.Paths.Pretty(filepath.Join(base, ".gemini")),
			Detail: "GEMINI_CLI_HOME moved", Destructive: true,
			Apply: func(ctx context.Context) error {
				_, err := runner.Output(ctx, m.Runner, geminiCmdAt(base, "extensions", "uninstall", geminiExtension))
				return err
			},
		})
	}
	return changes, keep
}

// planGeminiLink links the extension, or unlinks one zakwas linked when it
// is no longer wanted. Unlinking comes first, since gemini needs the manifest
// to uninstall.
//
// kept reports a link zakwas made that can't be removed yet because gemini
// is not installed: its manifest stays until the uninstall has run.
func (m *Module) planGeminiLink(wanted bool) (c *engine.Change, unlink, kept bool, err error) {
	have, err := m.geminiLinked()
	if err != nil {
		return nil, false, false, err
	}
	dir := m.geminiExtDir()
	ours := have != nil && have.Type == "link" && sameFile(have.Source) == sameFile(dir)
	switch {
	case wanted && have == nil:
		return &engine.Change{
			Action: engine.Create, Target: "gemini extension " + geminiExtension,
			Detail: "link " + m.Paths.Pretty(dir),
			Apply: func(ctx context.Context) error {
				_, err := runner.Output(ctx, m.Runner, m.geminiCmd("extensions", "link", dir, "--consent"))
				return err
			},
		}, false, false, nil
	case wanted && !ours:
		return nil, false, false, fmt.Errorf("the gemini extension %q is installed from %s, not linked to %s; remove it with `gemini extensions uninstall %s` or rename it", geminiExtension, m.Paths.Pretty(have.Source), m.Paths.Pretty(dir), geminiExtension)
	case !wanted && ours && !m.Runner.Installed("gemini"):
		return nil, false, true, nil
	case !wanted && ours:
		return &engine.Change{
			Action: engine.Remove, Target: "gemini extension " + geminiExtension, Detail: "nothing left to put in it", Destructive: true,
			Apply: func(ctx context.Context) error {
				_, err := runner.Output(ctx, m.Runner, m.geminiCmd("extensions", "uninstall", geminiExtension))
				return err
			},
		}, true, false, nil
	}
	return nil, false, false, nil
}

// planGemini converges the extension and the policy file. The context file,
// the subagents and the skills come from the instructions, profiles and
// skills planners, which write into the extension directory, so the link,
// which needs the manifest, is the last change.
func (m *Module) planGemini() ([]engine.Change, error) {
	wanted := m.Agents.Wants(config.ProviderGemini)
	statePath := m.geminiStatePath()
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("gemini: %s is a symlink; remove it", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	in := m.geminiInstaller()
	tracked, err := in.Tracked()
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	link, unlink, kept, err := m.planGeminiLink(wanted && m.geminiNeedsExtension())
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	if !wanted && len(tracked) == 0 && link == nil {
		return nil, nil
	}
	stale, staleKept := m.planGeminiStaleUnlinks(tracked)
	changes := stale
	keep := map[string]bool{}
	if unlink {
		changes = append(changes, *link)
	}
	if wanted {
		if !m.Runner.Installed("gemini") {
			return nil, errors.New("gemini: gemini is not installed")
		}
		files, err := m.geminiFiles()
		if err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		var errs []error
		for _, f := range files {
			keep[samePath(f.path)] = true
			c, err := in.Plan(f.path, f.content, 0o644)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if c != nil {
				c.Detail = strings.TrimPrefix(c.Detail+", "+f.detail, ", ")
				changes = append(changes, *c)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
	}
	for _, dst := range tracked {
		if keep[samePath(dst)] || staleKept[samePath(dst)] || kept && samePath(dst) == samePath(filepath.Join(m.geminiExtDir(), "gemini-extension.json")) {
			continue
		}
		c, err := m.planCopyRemove(in, statePath, dst, "gemini no longer declares it")
		if err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		changes = append(changes, c)
	}
	if link != nil && !unlink {
		changes = append(changes, *link)
	}
	for i := range changes {
		changes[i].Group = config.ProviderGemini
	}
	return changes, nil
}

// geminiWarnings names what Gemini CLI can't take of the declared servers.
func (m *Module) geminiWarnings() []string {
	if !m.Agents.Wants(config.ProviderGemini) {
		return nil
	}
	_, skipped := m.geminiServers()
	var out []string
	for _, name := range slices.Sorted(maps.Keys(skipped)) {
		out = append(out, fmt.Sprintf("gemini leaves out mcp server %s: %s", name, strings.Join(skipped[name], "; ")))
	}
	for _, s := range m.Agents.MCPFor(config.ProviderGemini) {
		if s.OAuth && skipped[s.Name] == nil {
			out = append(out, fmt.Sprintf("gemini mcp server %s signs in with OAuth: run /mcp auth %s in Gemini CLI", s.Name, s.Name))
		}
	}
	return out
}
