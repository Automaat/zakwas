//go:build windows

package install

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func TestLinksAreRefusedOnWindows(t *testing.T) {
	dir := t.TempDir()
	paths := config.Paths{Home: dir, Root: dir}

	c, err := PlanLink(paths, filepath.Join(dir, "src"), filepath.Join(dir, "dst"))

	if c != nil || !errors.Is(err, ErrSymlinksUnsupported) {
		t.Errorf("PlanLink = %v, %v; want ErrSymlinksUnsupported", c, err)
	}
	if err := Symlink("a", filepath.Join(dir, "b")); !errors.Is(err, ErrSymlinksUnsupported) {
		t.Errorf("Symlink = %v", err)
	}
}
