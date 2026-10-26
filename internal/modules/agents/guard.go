package agents

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

// claudeGuardRules are the Claude Code deny rules of the guard: no zakwas
// apply, upgrade or self-update, also with a flag before the command as in
// `zakwas -c zakwas.yaml apply` and run by path as in
// `~/.local/bin/zakwas apply`, and no edits to the config repo or to
// zakwas state. Claude Code applies deny rules in every permission mode.
// Edit rules reach Claude's own file tools, not what a shell command does,
// so the command rules are the ones that stop an apply. On Windows the
// same rules are written for the PowerShell tool as well, and name
// zakwas.exe.
func (m *Module) claudeGuardRules() []string {
	if !m.Guard || !m.Agents.GuardsProvider(config.ProviderClaude) {
		return nil
	}
	names := config.GuardShellNames(m.goos())
	rules := claudeShellRules("Bash", names)
	if m.goos() == "windows" {
		// PowerShell calls a program with &, which a rule sees as text.
		rules = append(rules, claudeShellRules("PowerShell", append(slices.Clone(names), "& zakwas"))...)
	}
	dirs, _ := m.guardedDirs()
	for _, dir := range append(dirs, filepath.Join(realPath(m.Paths.Home), ".local", "state", "zakwas")) {
		rules = append(rules, "Edit("+m.claudePath(dir)+"/**)")
	}
	return rules
}

// claudeShellRules denies each guarded command for each spelling of the
// binary, bare or followed by flags and arguments, in tool's rule syntax.
func claudeShellRules(tool string, names []string) []string {
	var rules []string
	for _, c := range config.GuardedCommands {
		for _, name := range names {
			for _, form := range []string{"", " *"} {
				rules = append(rules, tool+"("+name+" "+c+form+")", tool+"("+name+" * "+c+form+")")
			}
		}
	}
	return rules
}

// claudePath spells dir the way Claude Code's path rules do: ~/ for the
// home directory, // for an absolute path.
func (m *Module) claudePath(dir string) string {
	if rel, err := filepath.Rel(realPath(m.Paths.Home), dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return claudeAbsPath(filepath.ToSlash(dir))
}

// claudeAbsPath is the rule spelling of an absolute path in slash form. On
// Windows Claude Code turns C:\Users\alice into /c/Users/alice before it
// matches, so the rule for D:/repo is //d/repo.
func claudeAbsPath(slashed string) string {
	if len(slashed) >= 2 && slashed[1] == ':' && (slashed[0]|0x20 >= 'a' && slashed[0]|0x20 <= 'z') {
		return "//" + strings.ToLower(slashed[:1]) + slashed[2:]
	}
	return "/" + slashed
}

// guardedDirs are the directories Claude is denied edits to: the config
// repo's checkout, and its main checkout when that is another one, a
// worktree of it, for the main config and for each local layer. Agents work
// in worktrees, so those stay editable. A repo
// that holds the home directory can't be denied; the warning says so.
func (m *Module) guardedDirs() (dirs []string, warning string) {
	for _, r := range m.Paths.Roots() {
		root := realPath(r)
		top, main := repoCheckouts(root)
		candidates := []string{top, main}
		if top == "" {
			candidates = []string{root}
		}
		for _, d := range candidates {
			if contains(d, m.Paths.Home) {
				warning = "the config repo holds your home directory, so Claude's edits to it can't be denied"
				d = root
			}
			if contains(d, m.Paths.Home) {
				continue
			}
			if !hasDir(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	return dirs, warning
}

// goos is the operating system the guard is planned for.
func (m *Module) goos() string { return cmp.Or(m.GOOS, hostOS) }

func hasDir(dirs []string, d string) bool {
	for _, o := range dirs {
		if samePath(o) == samePath(d) {
			return true
		}
	}
	return false
}

// contains reports whether path is dir or below it.
func contains(dir, path string) bool {
	rel, err := filepath.Rel(realPath(dir), realPath(path))
	return err == nil && !strings.HasPrefix(rel, "..")
}

func realPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// repoCheckouts finds the git checkout holding dir and, when that is a
// linked worktree, the main checkout it belongs to. Both are empty outside
// a git repo.
func repoCheckouts(dir string) (top, main string) {
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			if info.IsDir() {
				return d, d
			}
			return d, mainCheckout(d)
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}

// mainCheckout follows a linked worktree's .git file to the repository it
// was added to; it falls back to the worktree itself.
func mainCheckout(worktree string) string {
	data, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		return worktree
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return worktree
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(worktree, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return worktree
	}
	dir := strings.TrimSpace(string(common))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitdir, dir)
	}
	dir = realPath(dir)
	if filepath.Base(dir) == ".git" {
		return filepath.Dir(dir)
	}
	return dir
}

var agentPermissionsCall = regexp.MustCompile("agentPermissions\\s+[\"`]opencode[\"`]")

// Warnings names the managed providers the guard can't protect: opencode
// reads its rules from the opencode.json a template renders with the
// agentPermissions function, and zakwas can't tell otherwise. It also names
// what each profile asks of a provider that can't express it.
func (m *Module) Warnings(context.Context) []string {
	out := append(m.profileWarnings(), m.opencodePromptWarnings()...)
	out = append(out, m.geminiWarnings()...)
	out = append(out, m.copilotWarnings()...)
	if !m.Guard {
		return out
	}
	if m.Agents.GuardsProvider(config.ProviderClaude) {
		if _, warning := m.guardedDirs(); warning != "" {
			out = append(out, "claude is only partly protected: "+warning)
		}
	}
	if m.Agents.GuardsProvider(config.ProviderCopilot) {
		out = append(out, "copilot can't be protected by rules: Copilot CLI has no user-level deny rules, so only zakwas's refusal to apply or upgrade from a Copilot session stops its agents from running zakwas apply")
	}
	if m.Agents.GuardsProvider(config.ProviderOpencode) && !m.rendersOpencodePermissions() {
		out = append(out, "opencode can't be protected: no template renders agentPermissions \"opencode\" into its opencode.json, so its agents can run zakwas apply")
	}
	return out
}

func (m *Module) rendersOpencodePermissions() bool {
	for _, src := range m.TemplateSources {
		if data, err := os.ReadFile(src); err == nil && agentPermissionsCall.Match(data) {
			return true
		}
	}
	return false
}

var opencodeToolChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// opencodePromptWarnings names the tools opencode must be told by hand to
// ask about: its permission object is part of an opencode.json that zakwas
// only renders pieces of, so it can't add a key there.
func (m *Module) opencodePromptWarnings() []string {
	var out []string
	for _, s := range m.Agents.MCPFor(config.ProviderOpencode) {
		for _, tool := range s.PromptTools() {
			key := opencodeToolChars.ReplaceAllString(s.Name, "_") + "_" + opencodeToolChars.ReplaceAllString(tool, "_")
			if !m.templatesAsk(key) {
				out = append(out, fmt.Sprintf("opencode won't ask before %s of MCP server %s: add %q: \"ask\" to the permission object of its opencode.json template", tool, s.Name, key))
			}
		}
	}
	return out
}

func (m *Module) templatesAsk(key string) bool {
	ask := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `"\s*:\s*"ask"`)
	for _, src := range m.TemplateSources {
		if data, err := os.ReadFile(src); err == nil && ask.Match(data) {
			return true
		}
	}
	return false
}

// copilotWarnings names what Copilot CLI can't take: it reads shell
// permission rules only from root-owned managed settings, not from any
// user-level file zakwas could write.
func (m *Module) copilotWarnings() []string {
	perms := m.Agents.PermissionsFor(config.ProviderCopilot)
	if len(perms) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("copilot can't express agents.permissions (%d for copilot): Copilot CLI keeps user-level shell rules only in root-owned managed settings, so its agents ask about every command", len(perms))}
}
