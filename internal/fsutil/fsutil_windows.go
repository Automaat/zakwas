//go:build windows

package fsutil

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	renameTries = 6
	renameWait  = 10 * time.Millisecond
)

func busy(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func rename(oldpath, newpath string) error {
	var err error
	for i := range renameTries {
		if err = os.Rename(oldpath, newpath); err == nil || !busy(err) {
			return err
		}
		clearReadOnly(newpath)
		time.Sleep(renameWait << i)
	}
	return err
}

func clearReadOnly(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o200 != 0 {
		return
	}
	_ = os.Chmod(path, info.Mode().Perm()|0o200)
}

func syncDir(string) error { return nil }
