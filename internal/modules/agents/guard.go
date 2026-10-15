package agents

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

// claudeGuardRules are the Claude Code deny rules of the guard: no zakwas
// apply, upgrade or self-update, also with a flag before the command as in
// `zakwas -c zakwas.yaml apply`, and no edits to the config repo or to
// zakwas state. Claude Code applies deny rules in every permission mode.
// Edit rules reach Claude's own file tools, not what a shell command does,
// so the command rules are the ones that stop an apply.
func (m *Module) claudeGuardRules() []string {
	if !m.Guard || !m.Agents.GuardsProvider(config.ProviderClaude) {
		return nil
	}
	var rules []string
	for _, c := range config.GuardedCommands {
		for _, form := range []string{"", " *"} {
			rules = append(rules, "Bash(zakwas "+c+form+")", "Bash(zakwas * "+c+form+")")
		}
	}
	dirs, _ := m.guardedDirs()
	for _, dir := range append(dirs, filepath.Join(realPath(m.Paths.Home), ".local", "state", "zakwas")) {
		rules = append(rules, "Edit("+m.claudePath(dir)+"/**)")
	}
	return rules
}

// claudePath spells dir the way Claude Code's path rules do: ~/ for the
// home directory, // for an absolute path.
func (m *Module) claudePath(dir string) string {
	if rel, err := filepath.Rel(realPath(m.Paths.Home), dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~/" + filepath.ToSlash(rel)
	}
	return "/" + filepath.ToSlash(dir)
}

// guardedDirs are the directories Claude is denied edits to: the config
// repo's checkout, and its main checkout when that is another one, a
// worktree of it. Agents work in worktrees, so those stay editable. A repo
// that holds the home directory can't be denied; the warning says so.
func (m *Module) guardedDirs() (dirs []string, warning string) {
	root := realPath(m.Paths.Root)
	top, main := repoCheckouts(root)
	for _, d := range []string{top, main} {
		if d == "" {
			continue
		}
		if contains(d, m.Paths.Home) {
			d = root
		}
		if contains(d, m.Paths.Home) {
			return dirs, "the config repo holds your home directory, so Claude's edits to it can't be denied"
		}
		if !hasDir(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		dirs = append(dirs, root)
	}
	return dirs, ""
}

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
// agentPermissions function, and zakwas can't tell otherwise.
func (m *Module) Warnings(context.Context) []string {
	if !m.Guard {
		return nil
	}
	var out []string
	if m.Agents.GuardsProvider(config.ProviderClaude) {
		if _, warning := m.guardedDirs(); warning != "" {
			out = append(out, "claude is only partly protected: "+warning)
		}
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
