//go:build windows

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func canSymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "t"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "t"), filepath.Join(dir, "l")); err != nil {
		t.Skipf("this Windows account can't create symlinks (no Developer Mode, not elevated): %v", err)
	}
}

func TestJunctionNeedsNoPrivilegeAndIsALink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "junction")

	if err := makeJunction(target, link); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !isLink(link, info) {
		t.Errorf("a junction (mode %v, tag %#x) is not a link", info.Mode(), reparseTag(link))
	}
	got, ok, err := LinkTarget(link, info)
	if err != nil || !ok || !sameTarget("windows", link, got, target) {
		t.Errorf("LinkTarget = %q, %v, %v; want %q", got, ok, err, target)
	}
	if data, err := os.ReadFile(filepath.Join(link, "SKILL.md")); err != nil || string(data) != "x" {
		t.Errorf("reading through the junction: %q, %v", data, err)
	}
	paths := config.Paths{Home: root, Root: root}
	if err := CheckParents(paths, filepath.Join(link, "child")); err == nil {
		t.Error("a destination behind a junction was accepted")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		t.Errorf("removing the junction touched its target: %v", err)
	}
}

func TestSymlinksAreRecognizedByTheirTarget(t *testing.T) {
	canSymlink(t)
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	dir := filepath.Join(root, "d")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{file, dir} {
		link := filepath.Join(root, "link-"+filepath.Base(target))
		if err := CreateLink(target, link); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := LinkTarget(link, info)
		if err != nil || !ok {
			t.Fatalf("LinkTarget(%s) = %q, %v, %v", link, got, ok, err)
		}
		if !SameTarget(link, got, target) {
			t.Errorf("Readlink gave %q for a link to %q", got, target)
		}
		if upper := strings.ToUpper(target); !SameTarget(link, got, upper) {
			t.Errorf("not the same as %q", upper)
		}
	}
}

func TestRequireLinksMatchesWhatThisMachineAllows(t *testing.T) {
	probe := t.TempDir()
	symlinks := os.Symlink(probe, filepath.Join(t.TempDir(), "l")) == nil

	fileErr, dirErr := RequireLinks(false), RequireLinks(true)

	t.Logf("symlinks allowed: %v; file links: %v; directory links: %v", symlinks, fileErr, dirErr)
	if symlinks && (fileErr != nil || dirErr != nil) {
		t.Errorf("symlinks work here but RequireLinks says %v / %v", fileErr, dirErr)
	}
	if !symlinks && fileErr == nil {
		t.Error("file links are reported available without symlink rights")
	}
	if dirErr != nil {
		t.Errorf("directory links failed even with a junction available: %v", dirErr)
	}
}

func TestPlanLinkConvergesFileAndDirectoryLinks(t *testing.T) {
	canSymlink(t)
	root, home := t.TempDir(), t.TempDir()
	paths := config.Paths{Home: home, Root: root}
	srcFile, srcDir := filepath.Join(root, "rc"), filepath.Join(root, "conf")
	if err := os.WriteFile(srcFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{srcFile: filepath.Join(home, "sub", "rc"), srcDir: filepath.Join(home, ".config", "conf")} {
		c, err := PlanLink(paths, src, dst)
		if err != nil || c == nil {
			t.Fatalf("PlanLink(%s) = %v, %v", src, c, err)
		}
		if err := c.Apply(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c, err := PlanLink(paths, src, dst); c != nil || err != nil {
			t.Errorf("a second plan of %s wants %v, %v", dst, c, err)
		}
	}
}
