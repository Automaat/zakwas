package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
)

// PlanLink returns the change that makes dst a symlink to src, or nil when
// it already is one. Another symlink at dst is replaced; a file is backed up
// first; a directory fails the plan.
func PlanLink(paths config.Paths, src, dst string) (*engine.Change, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("source %s: %w", src, err)
	}
	if err := CheckParents(paths, dst); err != nil {
		return nil, err
	}
	target := paths.Pretty(dst)
	link := func(context.Context) error { return Symlink(src, dst) }
	needLinks := func() error { return RequireLinks(srcInfo.IsDir()) }

	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := needLinks(); err != nil {
			return nil, err
		}
		return &engine.Change{Action: engine.Create, Target: target, Detail: "→ " + paths.Pretty(src), Apply: link}, nil
	case err != nil:
		return nil, err
	}

	if current, isLink, err := LinkTarget(dst, info); err != nil {
		return nil, err
	} else if isLink {
		if SameTarget(dst, current, src) {
			return nil, nil
		}
		if err := needLinks(); err != nil {
			return nil, err
		}
		return &engine.Change{
			Action: engine.Update, Target: target,
			Detail: fmt.Sprintf("relink %s → %s", current, paths.Pretty(src)),
			Apply: func(context.Context) error {
				if err := os.Remove(dst); err != nil {
					return err
				}
				return Symlink(src, dst)
			},
		}, nil
	}

	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; move it away before linking", dst)
	}
	if os.SameFile(info, srcInfo) {
		return nil, fmt.Errorf("%s is the source %s itself; link it somewhere else", dst, paths.Pretty(src))
	}
	if err := needLinks(); err != nil {
		return nil, err
	}
	return &engine.Change{
		Action: engine.Update, Target: target,
		Detail: fmt.Sprintf("back up to %s, link → %s", filepath.Base(BackupName(dst)), paths.Pretty(src)),
		Apply: func(context.Context) error {
			if _, err := Backup(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return Symlink(src, dst)
		},
	}, nil
}

// Symlink links dst to src (a junction on Windows when src is a directory
// and symlinks are denied), creating dst's parent directories.
func Symlink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return CreateLink(src, dst)
}
