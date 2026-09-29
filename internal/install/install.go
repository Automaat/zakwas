// Package install writes protected copies of managed files: write bits
// stripped and, optionally, the macOS immutable flag set, so dotfiles can only
// change through the repo and `zakwas apply`.
package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
)

// Installer writes protected copies. State is loaded from StatePath on first
// use when not set, so a broken state file fails only the modules using it.
type Installer struct {
	Paths     config.Paths
	State     *State
	StatePath string
	Immutable bool
}

// ReadOnly strips write bits, keeping read and execute bits of perm.
func ReadOnly(perm fs.FileMode) fs.FileMode {
	return perm.Perm() &^ 0o222
}

// Plan returns the change that makes dst a protected copy of want, or nil.
func (in *Installer) Plan(dst string, want []byte, perm fs.FileMode) (*engine.Change, error) {
	if err := in.loadState(); err != nil {
		return nil, err
	}
	if err := CheckParents(in.Paths, dst); err != nil {
		return nil, err
	}
	perm = ReadOnly(perm)
	target := in.Paths.Pretty(dst)
	change := func(a engine.Action, detail string, apply func(context.Context) error) *engine.Change {
		return &engine.Change{Action: a, Target: target, Detail: detail, Apply: apply}
	}

	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c := change(engine.Create, "", in.install(dst, want, perm, false))
		c.Diff = udiff.Unified("/dev/null", "repo", "", string(want))
		return c, nil
	case err != nil:
		return nil, err
	case info.IsDir():
		return nil, fmt.Errorf("%s is a directory; move it away first", dst)
	case info.Mode()&fs.ModeSymlink != 0:
		c := change(engine.Update, "replace symlink", in.install(dst, want, perm, false))
		linked, _ := os.ReadFile(dst)
		c.Diff = udiff.Unified(dst, "repo", string(linked), string(want))
		return c, nil
	}

	have, err := os.ReadFile(dst)
	if err != nil {
		return nil, err
	}
	withDiff := func(c *engine.Change) *engine.Change {
		c.Diff = udiff.Unified(dst, "repo", string(have), string(want))
		return c
	}
	if bytes.Equal(have, want) {
		reasons, err := in.protectionDrift(dst, info.Mode().Perm(), perm)
		if err != nil {
			return nil, err
		}
		if _, known := in.State.Get(dst); !known {
			reasons = append(reasons, "already matches, start tracking")
		}
		if len(reasons) == 0 {
			return nil, nil
		}
		return change(engine.Update, strings.Join(reasons, ", "), in.protect(dst, want, perm)), nil
	}

	recorded, known := in.State.Get(dst)
	if known && recorded == Sum(have) {
		return withDiff(change(engine.Update, "content", in.install(dst, want, perm, false))), nil
	}
	why := "edited in place"
	if !known {
		why = "not managed yet"
	}
	detail := fmt.Sprintf("%s, back up to %s", why, filepath.Base(BackupName(dst)))
	return withDiff(change(engine.Update, detail, in.install(dst, want, perm, true))), nil
}

// PlanRemove handles a destination zakwas once wrote but no longer manages. A
// copy it still recognizes is deleted, an edited one is backed up first, and
// anything that is no longer a regular file (e.g. now a link another module
// owns) is only dropped from the state.
func (in *Installer) PlanRemove(dst string) (*engine.Change, error) {
	if err := in.loadState(); err != nil {
		return nil, err
	}
	if !in.ownsPath(dst) {
		return in.PlanForget(dst, "no longer managed, not under $HOME or reached through a symlinked directory, left in place"), nil
	}
	target := in.Paths.Pretty(dst)
	forget := func(context.Context) error { return in.State.Forget(dst) }

	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Change{Action: engine.Remove, Target: target, Detail: "already gone, forget", Apply: forget}, nil
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return &engine.Change{Action: engine.Remove, Target: target, Detail: "no longer managed, left in place", Apply: forget}, nil
	}

	have, err := os.ReadFile(dst)
	if err != nil {
		return nil, err
	}
	if recorded, _ := in.State.Get(dst); recorded == Sum(have) {
		return &engine.Change{
			Action: engine.Remove, Target: target, Detail: "no longer managed",
			Apply: func(ctx context.Context) error {
				if err := unlock(dst); err != nil {
					return err
				}
				if err := os.Remove(dst); err != nil {
					return err
				}
				return forget(ctx)
			},
		}, nil
	}
	return &engine.Change{
		Action: engine.Remove, Target: target,
		Detail: "no longer managed, edited: back up to " + filepath.Base(BackupName(dst)),
		Apply: func(ctx context.Context) error {
			if _, err := Backup(dst); err != nil {
				return err
			}
			return forget(ctx)
		},
	}, nil
}

func (in *Installer) protectionDrift(dst string, have, want fs.FileMode) ([]string, error) {
	var reasons []string
	if have != want {
		reasons = append(reasons, fmt.Sprintf("mode %o → %o", have, want))
	}
	if !immutableSupported {
		return reasons, nil
	}
	locked, err := isImmutable(dst)
	if err != nil {
		return nil, err
	}
	switch {
	case in.Immutable && !locked:
		reasons = append(reasons, "set immutable")
	case !in.Immutable && locked:
		reasons = append(reasons, "clear immutable")
	}
	return reasons, nil
}

// install writes want to dst, first moving any existing file aside when
// backup is set. The immutable flag is lifted only for the swap.
func (in *Installer) install(dst string, want []byte, perm fs.FileMode, backup bool) func(context.Context) error {
	return func(context.Context) error {
		if err := unlock(dst); err != nil {
			return err
		}
		if backup {
			if _, err := Backup(dst); err != nil {
				return err
			}
		}
		if err := writeAtomic(dst, want, perm); err != nil {
			return err
		}
		return in.finish(dst, want)
	}
}

func (in *Installer) protect(dst string, want []byte, perm fs.FileMode) func(context.Context) error {
	return func(context.Context) error {
		if err := unlock(dst); err != nil {
			return err
		}
		if err := os.Chmod(dst, perm); err != nil {
			return err
		}
		return in.finish(dst, want)
	}
}

func (in *Installer) finish(dst string, want []byte) error {
	if in.Immutable {
		if err := setImmutable(dst, true); err != nil {
			return err
		}
	}
	return in.State.Record(dst, Sum(want))
}

func (in *Installer) loadState() error {
	if in.State != nil {
		return nil
	}
	s, err := LoadState(in.StatePath)
	if err != nil {
		return fmt.Errorf("state %s: %w", in.StatePath, err)
	}
	in.State = s
	return nil
}

// Tracked returns every destination the state records.
func (in *Installer) Tracked() ([]string, error) {
	if err := in.loadState(); err != nil {
		return nil, err
	}
	return in.State.Keys(), nil
}

// unlock clears the immutable flag on an existing regular file so it can be
// replaced; symlinks and missing files need nothing.
func unlock(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	return setImmutable(path, false)
}

// Unlock clears the immutable flag on every file under root, for tests and for
// tearing down a managed tree.
func Unlock(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return unlock(path)
	})
}

// PlanForget drops dst from the state without touching the file.
func (in *Installer) PlanForget(dst, detail string) *engine.Change {
	return &engine.Change{
		Action: engine.Remove, Target: in.Paths.Pretty(dst), Detail: detail,
		Apply: func(context.Context) error { return in.State.Forget(dst) },
	}
}

// ownsPath reports whether dst is a path zakwas could have written itself:
// under $HOME with no symlinked directory in between. A directory that moved
// to links resolves into the repo, and deleting "through" it would remove
// the repo's source file.
func (in *Installer) ownsPath(dst string) bool {
	home := filepath.Clean(in.Paths.Home)
	for dir := filepath.Dir(filepath.Clean(dst)); dir != home; dir = filepath.Dir(dir) {
		if !within(dir, home) {
			return false
		}
		info, err := os.Lstat(dir)
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// CheckParents refuses a destination reached through a symlinked directory
// under $HOME, or one resolving into the repo: writing through it would
// change the link's target, e.g. repo sources after a directory moved from
// links to files.
func CheckParents(paths config.Paths, dst string) error {
	home := filepath.Clean(paths.Home)
	for dir := filepath.Dir(filepath.Clean(dst)); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err == nil && info.Mode()&fs.ModeSymlink != 0 &&
			(within(dir, home) || resolvesInto(dir, paths.Root)) {
			return fmt.Errorf("parent %s of %s is a symlink; remove it", paths.Pretty(dir), paths.Pretty(dst))
		}
		if filepath.Dir(dir) == dir {
			return nil
		}
	}
}

func within(path, dir string) bool {
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

func resolvesInto(link, root string) bool {
	if root == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(link)
	root = filepath.Clean(root)
	return err == nil && (resolved == root || within(resolved, root))
}

func backupName(path string, n int) string {
	if n == 0 {
		return path + ".zakwas-bak"
	}
	return fmt.Sprintf("%s.zakwas-bak.%d", path, n)
}

// BackupName is the name Backup would pick right now, for plan output. Any
// Lstat error ends the search: e.g. ENAMETOOLONG would repeat for every N.
func BackupName(path string) string {
	for n := 0; ; n++ {
		name := backupName(path, n)
		if _, err := os.Lstat(name); err != nil {
			return name
		}
	}
}

// Backup moves path to the first free <path>.zakwas-bak[.N] and makes the copy
// writable. The name is taken at apply time with a hard link, which fails
// instead of overwriting, so no earlier backup is ever clobbered. Only
// regular files: link(2) on macOS follows symlinks, so a symlink's backup
// would alias its target and the chmod would change the target.
func Backup(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file, not backing it up", path)
	}
	if err := unlock(path); err != nil {
		return "", err
	}
	for n := 0; ; n++ {
		name := backupName(path, n)
		err := os.Link(path, name)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := os.Remove(path); err != nil {
			return "", err
		}
		return name, os.Chmod(name, 0o644)
	}
}

// writeAtomic replaces dst via rename so a failed write never leaves a
// half-written file behind.
func writeAtomic(dst string, data []byte, perm fs.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".zakwas-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	return syncDir(filepath.Dir(dst))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
