package agents

import (
	"errors"
	"os"
	"syscall"
)

// replaceDir moves the freshly made dir next into place at dir. A copy
// already there is exchanged atomically, so dir is never missing for a
// concurrent plan; next then holds the old copy, which the caller removes.
func replaceDir(next, dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return os.Rename(next, dir)
	}
	err := exchangeDirs(next, dir)
	if errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return renameAside(next, dir)
	}
	return err
}

// renameAside is the fallback for filesystems without an atomic exchange:
// it renames the old copy to next+".old", renames next in, and removes the
// old copy last, so dir is missing only between the two renames.
func renameAside(next, dir string) error {
	aside := next + ".old"
	if err := os.Rename(dir, aside); err != nil {
		return err
	}
	if err := os.Rename(next, dir); err != nil {
		return errors.Join(err, os.Rename(aside, dir))
	}
	return os.RemoveAll(aside)
}
