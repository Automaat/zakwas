// Package testutil holds helpers shared by tests.
package testutil

import (
	"runtime"
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
