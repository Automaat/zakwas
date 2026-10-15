// Package lock serializes the zakwas runs that change the system.
//
// It is an flock(2) on a file under the state dir: the kernel drops the lock
// when the holder exits, however it exits, so a crashed or killed run never
// leaves a stale lock to clean up.
package lock

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The holder writes its identity right after locking, so a blocked run waits
// holderSettle before reading it and retries while the file is still empty.
const (
	holderSettle = 50 * time.Millisecond
	holderReads  = 5
)

// Path is the lock file for a given home directory.
func Path(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "lock")
}

// Holder identifies the run that owns the lock. Both fields are best effort.
// A run that can't write the lock file records nothing, and the file may
// still name an earlier run. A run that can't write it either reports the
// zero Holder instead of reading it, and one that can reports it only while
// the recorded pid is alive.
type Holder struct {
	PID     int
	Command string
}

func (h Holder) String() string {
	switch {
	case h.PID > 0 && h.Command != "":
		return fmt.Sprintf("%q (pid %d)", h.Command, h.PID)
	case h.PID > 0:
		return fmt.Sprintf("pid %d", h.PID)
	}
	return "another zakwas run"
}

// BusyError reports that another run holds the lock and waiting was off.
type BusyError struct{ Holder Holder }

func (e *BusyError) Error() string {
	return "another run is in progress: " + e.Holder.String() + "; not waiting (--no-wait)"
}

// UnavailableError reports that the lock file can't be created or opened
// because the state dir or the file denies access. Err is the original
// error, not the one from the read-only retry.
type UnavailableError struct {
	Path string
	Err  error
}

func (e *UnavailableError) Error() string {
	reason := e.Err
	var pe *fs.PathError
	if errors.As(e.Err, &pe) {
		reason = pe.Err
	}
	return "can't take the run lock " + e.Path + ": " + reason.Error()
}

func (e *UnavailableError) Unwrap() error { return e.Err }

func denied(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS)
}

// Lock is a held lock; Release it when the run is done. Releasing a nil
// Lock is a no-op.
type Lock struct {
	f        *os.File
	writable bool
}

// Options tune Acquire. NoWait fails with *BusyError instead of waiting.
// OnWait is called once, with the holder, before the first wait. Poll is the
// retry interval while waiting (default 200ms).
type Options struct {
	NoWait bool
	OnWait func(Holder)
	Poll   time.Duration
}

// Acquire takes the lock for command, waiting while another run holds it
// until ctx is done.
func Acquire(ctx context.Context, home, command string, opts Options) (*Lock, error) {
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		if denied(err) {
			return nil, &UnavailableError{Path: path, Err: err}
		}
		return nil, err
	}
	writable := true
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if denied(err) {
		writable = false
		var roErr error
		if f, roErr = os.Open(path); roErr != nil {
			return nil, &UnavailableError{Path: path, Err: err}
		}
	} else if err != nil {
		return nil, err
	}
	poll := opts.Poll
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	waited := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return nil, errors.Join(err, f.Close())
		}
		if !waited {
			var h Holder
			if writable {
				h = settledHolder(path)
			}
			if opts.NoWait {
				return nil, errors.Join(&BusyError{h}, f.Close())
			}
			if opts.OnWait != nil {
				opts.OnWait(h)
			}
			waited = true
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), f.Close())
		case <-time.After(poll):
		}
	}
	l := &Lock{f: f, writable: writable}
	if err := l.writeHolder(command); err != nil {
		return nil, errors.Join(err, l.Release())
	}
	return l, nil
}

func (l *Lock) writeHolder(command string) error {
	if !l.writable {
		return nil
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	_, err := fmt.Fprintf(l.f, "%d\n%s\n", os.Getpid(), command)
	return err
}

// Release drops the lock; closing the file does it.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}

func settledHolder(path string) Holder {
	var h Holder
	for range holderReads {
		time.Sleep(holderSettle / 2)
		if h = readHolder(path); h.PID > 0 {
			break
		}
	}
	return h
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readHolder(path string) Holder {
	data, err := os.ReadFile(path)
	if err != nil {
		return Holder{}
	}
	pid, command, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	n, err := strconv.Atoi(pid)
	if err != nil {
		return Holder{}
	}
	if !alive(n) {
		return Holder{}
	}
	return Holder{PID: n, Command: strings.TrimSpace(command)}
}
