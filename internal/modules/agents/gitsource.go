package agents

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/runner"
)

// gitCmd runs git from /, never prompting for credentials.
func gitCmd(args ...string) runner.Cmd {
	return runner.Cmd{Name: "git", Args: args, Dir: "/", Env: []string{"GIT_TERMINAL_PROMPT=0"}}
}

// gitClone clones src, a GitHub 'owner/repo' or URL with an optional '#ref',
// shallowly into cacheDir/name. It clones next to the final dir and then
// swaps it in without a gap, so a failed clone leaves the previous copy
// alone and a concurrent plan never sees the dir missing.
func gitClone(ctx context.Context, r runner.Runner, cacheDir, name, src string) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	stale, _ := filepath.Glob(filepath.Join(cacheDir, "."+name+".zakwas-*"))
	for _, dir := range stale {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp(cacheDir, "."+name+".zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	url, ref := gitSource(src)
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	if err := runner.Check(ctx, r, gitCmd(append(args, "--", url, tmp)...)); err != nil {
		return err
	}
	return replaceDir(tmp, filepath.Join(cacheDir, name))
}

// gitPull moves a clone made by gitClone to the newest commit of its ref.
// git is pinned to the clone's own .git, so it can never walk up into an
// enclosing repo such as a dotfiles $HOME.
func gitPull(ctx context.Context, r runner.Runner, dir, src string) error {
	_, ref := gitSource(src)
	if ref == "" {
		ref = "HEAD"
	}
	repo := []string{"--git-dir=" + filepath.Join(dir, ".git"), "--work-tree=" + dir}
	if err := runner.Check(ctx, r, gitCmd(append(repo, "fetch", "--quiet", "--depth", "1", "origin", ref)...)); err != nil {
		return err
	}
	return runner.Check(ctx, r, gitCmd(append(repo, "reset", "--quiet", "--hard", "FETCH_HEAD")...))
}
