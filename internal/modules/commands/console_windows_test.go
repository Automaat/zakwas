//go:build windows

package commands

import (
	"testing"

	"golang.org/x/sys/windows"
)

// ensureConsole gives the test process a console when it has none, as a CI
// runner's has not: PowerShell takes its output encoding from the console
// code page, which only exists with a console. It skips the test if no
// console can be made.
func ensureConsole(t testing.TB) {
	t.Helper()
	if cp, _ := windows.GetConsoleOutputCP(); cp != 0 {
		return
	}
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	if r, _, err := kernel32.NewProc("AllocConsole").Call(); r == 0 {
		t.Skipf("no console can be allocated here, so PowerShell has no code page to take UTF-8 from: %v", err)
	}
	t.Cleanup(func() { _, _, _ = kernel32.NewProc("FreeConsole").Call() })
}
