// Package links symlinks repo files into the home directory, for configs
// that apps must be able to write. Everything else belongs in files.
package links

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

type Module struct {
	Links []config.Link
	Paths config.Paths
}

func (m *Module) Name() string { return "links" }

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	for _, l := range m.Links {
		src, dst := m.Paths.Src(l.Src), m.Paths.Dst(l.Dst)
		c, err := m.plan(src, dst)
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return changes, nil
}

func (m *Module) plan(src, dst string) (*engine.Change, error) {
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("source %s: %w", src, err)
	}
	if err := install.CheckParents(m.Paths, dst); err != nil {
		return nil, err
	}
	target := m.Paths.Pretty(dst)
	link := func(ctx context.Context) error { return symlink(src, dst) }

	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Change{Action: engine.Create, Target: target, Detail: "→ " + m.Paths.Pretty(src), Apply: link}, nil
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
			Detail: fmt.Sprintf("relink %s → %s", current, m.Paths.Pretty(src)),
			Apply: func(ctx context.Context) error {
				if err := os.Remove(dst); err != nil {
					return err
				}
				return symlink(src, dst)
			},
		}, nil
	}

	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; move it away before linking", dst)
	}
	return &engine.Change{
		Action: engine.Update, Target: target,
		Detail: fmt.Sprintf("back up to %s, link → %s", filepath.Base(install.BackupName(dst)), m.Paths.Pretty(src)),
		Apply: func(ctx context.Context) error {
			if _, err := install.Backup(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return symlink(src, dst)
		},
	}, nil
}

func symlink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Symlink(src, dst)
}
