// Package files installs repo files into the home directory as protected,
// read-only copies. Changes go through the repo and `zakwas apply`.
package files

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/platform"
)

// Module installs Files. Keep lists destinations the shared installer writes
// for other modules (templates), so they aren't treated as orphans.
type Module struct {
	Files     []config.Link
	Keep      []string
	Paths     config.Paths
	Installer *install.Installer
	// Immutable is protect.immutable as configured, before the platform
	// decides whether the installer can honor it.
	Immutable bool
	// GOOS is the operating system the plan is for; empty means this one.
	GOOS string
}

func (m *Module) Name() string { return "files" }

// Warnings tells once that protect.immutable can't lock files here. Files
// is the one module every config has, so it carries the notice for the
// templates and agents instruction copies too.
func (m *Module) Warnings(context.Context) []string {
	if !m.Immutable || platform.FeatureSupported("protect.immutable", cmp.Or(m.GOOS, platform.Current())) {
		return nil
	}
	return []string{fmt.Sprintf("protect.immutable is %s-only; files are read-only copies", platform.Name(platform.Darwin))}
}

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	wanted := map[string]bool{}
	for _, k := range m.Keep {
		wanted[k] = true
	}
	var changes []engine.Change
	for _, f := range m.Files {
		pairs, err := expand(m.Paths.Src(f.Src), m.Paths.Dst(f.Dst))
		if err != nil {
			return nil, err
		}
		for _, p := range pairs {
			wanted[p.dst] = true
			c, err := m.plan(p)
			if err != nil {
				return nil, err
			}
			if c != nil {
				changes = append(changes, *c)
			}
		}
	}
	tracked, err := m.Installer.Tracked()
	if err != nil {
		return nil, err
	}
	for _, dst := range tracked {
		if wanted[dst] {
			continue
		}
		if sameFileAsAny(dst, wanted) {
			changes = append(changes, *m.Installer.PlanForget(dst, "renamed, same file as a managed path"))
			continue
		}
		c, err := m.Installer.PlanRemove(dst)
		if err != nil {
			return nil, err
		}
		changes = append(changes, *c)
	}
	return changes, nil
}

// sameFileAsAny catches an old name that is still the very file a wanted
// path points at, e.g. a case-only rename on case-insensitive APFS, where
// removing the old name would delete the managed file.
func sameFileAsAny(dst string, wanted map[string]bool) bool {
	old, err := os.Lstat(dst)
	if err != nil {
		return false
	}
	for w := range wanted {
		if info, err := os.Lstat(w); err == nil && os.SameFile(old, info) {
			return true
		}
	}
	return false
}

func (m *Module) plan(p pair) (*engine.Change, error) {
	info, err := os.Stat(p.src)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p.src)
	if err != nil {
		return nil, err
	}
	return m.Installer.Plan(p.dst, data, info.Mode().Perm())
}

type pair struct{ src, dst string }

// expand maps a source file to itself, or a source directory to every file
// inside it. Extra files already in a destination directory are left alone.
func expand(src, dst string) ([]pair, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []pair{{src, dst}}, nil
	}
	var pairs []pair
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		pairs = append(pairs, pair{path, filepath.Join(dst, rel)})
		return nil
	})
	return pairs, err
}
