//go:build darwin || linux

package agents

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExchangeDirsSwapsAtomically(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	writeFile(t, filepath.Join(a, "a.txt"), "a")
	writeFile(t, filepath.Join(b, "b.txt"), "b")
	err := exchangeDirs(a, b)
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) {
		t.Skipf("this filesystem can't exchange directories: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a, "b.txt")); err != nil {
		t.Errorf("a does not hold b's content: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b, "a.txt")); err != nil {
		t.Errorf("b does not hold a's content: %v", err)
	}
}
