package agents

import (
	"errors"
	"os"
	"syscall"

	"github.com/Automaat/zakwas/internal/fsutil"
)

// replaceDir moves the freshly made dir next into place at dir. A copy
// already there is exchanged atomically, so dir is never missing for a
// concurrent plan; next then holds the old copy, which the caller removes.
// FAT volumes answer the exchange of two non-empty dirs with ENOTEMPTY, so
// that, like ENOTSUP and EINVAL, falls back to renameAside, which keeps the
// old copy when anything else fails.
func replaceDir(next, dir string) error {
	return replaceDirWith(exchangeDirs, next, dir)
}

func replaceDirWith(exchange func(a, b string) error, next, dir string) error {
	var err error
	for range 2 {
		if _, serr := os.Lstat(dir); errors.Is(serr, os.ErrNotExist) {
			if err = fsutil.Rename(next, dir); !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
				return err
			}
			continue
		}
		err = exchange(next, dir)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, errors.ErrUnsupported), errors.Is(err, syscall.ENOTSUP), errors.Is(err, syscall.EINVAL), errors.Is(err, syscall.ENOTEMPTY):
			return renameAside(next, dir)
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	return err
}

// renameAside is the fallback for filesystems without an atomic exchange:
// it renames the old copy to next+".old", renames next in, and removes the
// old copy last, so dir is missing only between the two renames. A failed
// removal is left for the next fetch to clean up.
func renameAside(next, dir string) error {
	aside := next + ".old"
	if err := fsutil.Rename(dir, aside); err != nil {
		return err
	}
	if err := fsutil.Rename(next, dir); err != nil {
		return errors.Join(err, fsutil.Rename(aside, dir))
	}
	_ = os.RemoveAll(aside)
	return nil
}
