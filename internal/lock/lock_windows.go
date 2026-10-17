//go:build windows

package lock

import (
	"errors"
	"io/fs"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

// The locked byte sits far past the holder record: LockFileEx locks are
// mandatory, so locking the record itself would stop a blocked run from
// reading who holds the lock.
const lockOffsetHigh = 0x7fffffff

func lockRegion() *windows.Overlapped {
	return &windows.Overlapped{OffsetHigh: lockOffsetHigh}
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".zakwas-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	return errors.Join(f.Close(), os.Remove(name)) == nil
}

func denied(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

func tryLock(f *os.File) (busy bool, err error) {
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRegion())
	if err == nil {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return true, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRegion())
}

func alive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == 259
}
