// Package testutil holds helpers shared by tests.
package testutil

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// SkipOnWindows skips the test on Windows. reason says what the test needs
// that Windows lacks, e.g. "POSIX file modes".
func SkipOnWindows(t testing.TB, reason string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("skipped on Windows: " + reason)
	}
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
