// Package testutil holds helpers shared by tests across packages.
package testutil

import (
	"runtime"
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
