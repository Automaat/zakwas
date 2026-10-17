// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// RequireDarwin skips the test off macOS. reason names the macOS behavior
// the test needs, so the skip says what is not covered.
func RequireDarwin(t testing.TB, reason string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("needs macOS (%s); this is %s", reason, runtime.GOOS)
	}
}

// RequireLinux skips the test off Linux.
func RequireLinux(t testing.TB, reason string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("needs Linux (%s); this is %s", reason, runtime.GOOS)
	}
}

// SkipOnWindows skips the test on Windows. reason says what the test needs
// that Windows lacks, e.g. "POSIX file modes".
func SkipOnWindows(t testing.TB, reason string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipped on Windows: " + reason)
	}
}

// RequireSymlinks skips the test on a machine that denies creating symlinks,
// which on Windows needs Developer Mode or an elevated account. It checks by
// creating one in a scratch directory, once per test binary.
func RequireSymlinks(t testing.TB) {
	t.Helper()
	if err := SymlinkDenied(); err != nil {
		t.Skipf("this Windows account can't create symlinks (no Developer Mode, not elevated): %v", err)
	}
}

// SymlinkDenied is why this machine can't create a symlink, or nil. Only
// Windows ever denies one.
func SymlinkDenied() error {
	if runtime.GOOS != "windows" {
		return nil
	}
	symlinkOnce.Do(func() { symlinkErr = probeSymlink() })
	return symlinkErr
}

var (
	symlinkOnce sync.Once
	symlinkErr  error
)

func probeSymlink() error {
	dir, err := os.MkdirTemp("", "zakwas-symlinkprobe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		return err
	}
	return os.Symlink(target, filepath.Join(dir, "l"))
}

// Abs turns a slash path rooted at "/" into one that is absolute on this
// platform: unchanged on Unix, "C:\h\x" for "/h/x" on Windows. Paths that
// are already absolute there, or relative, pass through.
func Abs(path string) string {
	if runtime.GOOS != "windows" || !strings.HasPrefix(path, "/") {
		return path
	}
	return `C:` + filepath.FromSlash(path)
}

// SetHome points the process's home directory at home for the test: HOME on
// Unix, USERPROFILE on Windows, which is where os.UserHomeDir looks.
func SetHome(t testing.TB, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// SamePerm compares file modes the way this platform can express them:
// all permission bits on Unix, only whether the owner may write on Windows,
// where a file is either read-only (0444) or not (0666).
func SamePerm(got, want fs.FileMode) bool {
	if runtime.GOOS == "windows" {
		return got.Perm()&0o200 == want.Perm()&0o200
	}
	return got.Perm() == want.Perm()
}

// Esc escapes backslashes so a Windows path can sit inside a JSON or TOML
// basic string written into a test fixture. Other paths pass through.
func Esc(path string) string {
	return strings.ReplaceAll(path, `\`, `\\`)
}
