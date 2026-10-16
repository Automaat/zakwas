//go:build !windows

package fsutil

import (
	"errors"
	"os"
)

func rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
