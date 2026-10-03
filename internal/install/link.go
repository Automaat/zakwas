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
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("source %s: %w", src, err)
	}
	if err := CheckParents(paths, dst); err != nil {
		return nil, err
	}
	target := paths.Pretty(dst)
	link := func(context.Context) error { return Symlink(src, dst) }

	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Change{Action: engine.Create, Target: target, Detail: "→ " + paths.Pretty(src), Apply: link}, nil
	case err != nil:
		return nil, err
	}

	if info.Mode()&fs.ModeSymlink != 0 {
		current, err := os.Readlink(dst)
		if err != nil {
			return nil, err
		}
		if current == src {
			return nil, nil
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

// Symlink links dst to src, creating dst's parent directories.
func Symlink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(src, dst)
}
