package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

func TestFilesAndDirectories(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("zsh/zshrc", "zsh", 0o644)
	write("bin/tool", "#!/bin/sh", 0o755)
	write("ghostty/themes/nord", "a", 0o644)
	write("ghostty/themes/sub/dark", "b", 0o644)

	state, err := install.LoadState(install.StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, Root: root}
	m := &Module{
		Paths:     paths,
		Installer: &install.Installer{Paths: paths, State: state},
		Files: []config.Link{
			{Src: "zsh/zshrc", Dst: "~/.zshrc"},
			{Src: "bin/tool", Dst: "~/.local/bin/tool"},
			{Src: "ghostty/themes", Dst: "~/.config/ghostty/themes"},
		},
	}
	ctx := context.Background()

	changes, err := m.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 4 {
		t.Fatalf("changes = %v", changes)
	}
	if err := engine.Apply(ctx, func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}

	want := map[string]os.FileMode{
		".zshrc":                          0o444,
		".local/bin/tool":                 0o555,
		".config/ghostty/themes/nord":     0o444,
		".config/ghostty/themes/sub/dark": 0o444,
	}
	for rel, mode := range want {
		info, err := os.Lstat(filepath.Join(home, rel))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v; want regular file %o", rel, info, err, mode)
		}
	}
	if again, err := m.Plan(ctx); err != nil || len(again) != 0 {
		t.Errorf("not idempotent: %v, %v", again, err)
	}
}

func TestMissingSource(t *testing.T) {
	home := t.TempDir()
	state, _ := install.LoadState(install.StatePath(home))
	paths := config.Paths{Home: home, Root: t.TempDir()}
	m := &Module{
		Paths: paths, Installer: &install.Installer{Paths: paths, State: state},
		Files: []config.Link{{Src: "nope", Dst: "~/x"}},
	}
	if _, err := m.Plan(context.Background()); err == nil {
		t.Error("expected error")
	}
}

func TestRemovesFilesNoLongerManaged(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Cleanup(func() { _ = install.Unlock(home) })
	for rel, body := range map[string]string{"zshrc": "z", "vimrc": "v", "themes/a": "a", "themes/b": "b"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state, err := install.LoadState(install.StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, Root: root}
	in := &install.Installer{Paths: paths, State: state, Immutable: true}
	template := filepath.Join(home, ".config/k9s/config.yaml")
	if c, err := in.Plan(template, []byte("rendered"), 0o644); err != nil || c.Apply(context.Background()) != nil {
		t.Fatalf("template setup: %v", err)
	}
	m := &Module{Paths: paths, Installer: in, Keep: []string{template}, Files: []config.Link{
		{Src: "zshrc", Dst: "~/.zshrc"},
		{Src: "vimrc", Dst: "~/.vimrc"},
		{Src: "themes", Dst: "~/.themes"},
	}}
	apply := func() []engine.Change {
		changes, err := m.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
			t.Fatal(err)
		}
		return changes
	}
	apply()

	m.Files = m.Files[:1]
	m.Files = append(m.Files, config.Link{Src: "themes", Dst: "~/.themes"})
	if err := os.Remove(filepath.Join(root, "themes/b")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range apply() {
		got = append(got, string(c.Action)+" "+c.Target)
	}
	want := []string{"- ~/.themes/b", "- ~/.vimrc"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for _, rel := range []string{".vimrc", ".themes/b"} {
		if _, err := os.Lstat(filepath.Join(home, rel)); err == nil {
			t.Errorf("%s still exists", rel)
		}
	}
	if _, err := os.Stat(template); err != nil {
		t.Errorf("template output must survive: %v", err)
	}
	if again, _ := m.Plan(context.Background()); len(again) != 0 {
		t.Errorf("not converged: %v", again)
	}
}

func newFilesModule(t *testing.T, sources map[string]string) (*Module, string, string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	t.Cleanup(func() { _ = install.Unlock(home) })
	for rel, body := range sources {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state, err := install.LoadState(install.StatePath(home))
	if err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{Home: home, Root: root}
	return &Module{Paths: paths, Installer: &install.Installer{Paths: paths, State: state, Immutable: true}}, home, root
}

func applyModule(t *testing.T, m *Module) []engine.Change {
	t.Helper()
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestDirMovedToLinksKeepsRepoFiles(t *testing.T) {
	m, home, root := newFilesModule(t, map[string]string{"d/a.md": "source"})
	m.Files = []config.Link{{Src: "d", Dst: "~/.cfg/d"}}
	applyModule(t, m)

	installed := filepath.Join(home, ".cfg/d")
	if err := install.Unlock(installed); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "d"), installed); err != nil {
		t.Fatal(err)
	}
	m.Files = nil
	changes := applyModule(t, m)

	if len(changes) != 1 || !strings.Contains(changes[0].Detail, "left in place") {
		t.Fatalf("changes = %v", changes)
	}
	if got, err := os.ReadFile(filepath.Join(root, "d/a.md")); err != nil || string(got) != "source" {
		t.Fatalf("repo source file damaged: %q, %v", got, err)
	}
	if again, _ := m.Plan(context.Background()); len(again) != 0 {
		t.Errorf("not converged: %v", again)
	}
}

func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "CaseProbe")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	if rmErr := os.Remove(p); rmErr != nil {
		t.Fatal(rmErr)
	}
	return err == nil
}

func TestCaseOnlyRenameKeepsFile(t *testing.T) {
	m, home, root := newFilesModule(t, map[string]string{"themes/Nord": "palette"})
	if !caseInsensitive(t, home) {
		t.Skip("case-sensitive filesystem")
	}
	m.Files = []config.Link{{Src: "themes", Dst: "~/.themes"}}
	applyModule(t, m)

	if err := os.Rename(filepath.Join(root, "themes/Nord"), filepath.Join(root, "themes/nord")); err != nil {
		t.Fatal(err)
	}
	applyModule(t, m)

	if got, err := os.ReadFile(filepath.Join(home, ".themes/nord")); err != nil || string(got) != "palette" {
		t.Fatalf("managed file lost after case-only rename: %q, %v", got, err)
	}
	if again, _ := m.Plan(context.Background()); len(again) != 0 {
		t.Errorf("not converged: %v", again)
	}
}
