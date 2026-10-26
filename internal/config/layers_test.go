package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/testutil"
)

func writeLayer(t *testing.T, name, body string) Layer {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Layer{Name: name, Path: p}
}

func TestParseLayeredMerges(t *testing.T) {
	root := writeConfig(t, `
files: [{src: a, dst: ~/.a}]
links: [{src: l, dst: ~/.l}]
templates:
  vars: {theme: dark}
  files: [{src: t.tmpl, dst: ~/.t}]
defaults: [{domain: com.apple.dock, key: autohide, value: true}]
system:
  dirs: [{path: ~/one}]
commands: [{name: root-cmd, check: "true", run: "true"}]
launchd:
  - {label: com.example.root, program: [/bin/true], schedule: {every: 1h}}
`)
	absSrc := testutil.Abs("/abs/src")
	first := writeLayer(t, "first", `
files: [{src: f, dst: ~/.first}, {src: '`+absSrc+`', dst: ~/.abs}]
links: [{src: fl, dst: ~/.firstl}]
templates:
  vars: {editor: vim}
  files: [{src: ft.tmpl, dst: ~/.ft}]
defaults: [{domain: com.apple.finder, key: ShowPathbar, value: true}]
system:
  dirs: [{path: ~/two}]
  sshKey: {path: ~/.ssh/id_first}
commands: [{name: first-cmd, check: "true", run: "true"}]
launchd:
  - {label: com.example.first, program: [/bin/true], schedule: {every: 1h}}
`)
	second := writeLayer(t, "second", `
files: [{src: s, dst: ~/.second}]
system:
  sudoTouchID: true
  dirs: [{path: ~/three}]
commands: [{name: second-cmd, check: "true", run: "true"}]
`)
	home := t.TempDir()

	c, err := ParseLayered(mustReadBytes(t, root), root, home, platform.Darwin, []Layer{first, second})
	if err != nil {
		t.Fatal(err)
	}

	var dsts []string
	for _, f := range c.Files {
		dsts = append(dsts, f.Dst)
	}
	if got, want := strings.Join(dsts, " "), "~/.a ~/.first ~/.abs ~/.second"; got != want {
		t.Errorf("files = %q, want root first, then layers in order: %q", got, want)
	}
	if got := c.Files[1].Src; got != filepath.Join(filepath.Dir(first.Path), "f") {
		t.Errorf("a layer's relative src = %q, want it under the layer's directory", got)
	}
	if got := c.Files[2].Src; got != absSrc {
		t.Errorf("an absolute src = %q, want it untouched", got)
	}
	if got := c.Files[0].Src; got != "a" {
		t.Errorf("the main config's src = %q, want it untouched", got)
	}
	if got := c.Links[1].Src; got != filepath.Join(filepath.Dir(first.Path), "fl") {
		t.Errorf("a layer's link src = %q", got)
	}
	if got := c.Templates.Files[1].Src; got != filepath.Join(filepath.Dir(first.Path), "ft.tmpl") {
		t.Errorf("a layer's template src = %q", got)
	}
	if len(c.Templates.Vars) != 2 || c.Templates.Vars["theme"] != "dark" || c.Templates.Vars["editor"] != "vim" {
		t.Errorf("templates.vars = %v", c.Templates.Vars)
	}
	if len(c.Defaults) != 2 || len(c.Launchd) != 2 || len(c.System.Dirs) != 3 {
		t.Errorf("defaults %d, launchd %d, dirs %d, want 2, 2, 3", len(c.Defaults), len(c.Launchd), len(c.System.Dirs))
	}
	var names []string
	for _, cmd := range c.Commands {
		names = append(names, cmd.Name)
	}
	if got, want := strings.Join(names, " "), "root-cmd first-cmd second-cmd"; got != want {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if c.System.SSHKey == nil || c.System.SSHKey.Path != "~/.ssh/id_first" {
		t.Errorf("sshKey = %+v", c.System.SSHKey)
	}
	if !c.System.SudoTouchID {
		t.Error("sudoTouchID from a layer is not on")
	}
	if got := c.LayerRefs(); len(got) != 2 || got[0] != first || got[1] != second {
		t.Errorf("LayerRefs = %v", got)
	}
	if got := c.Paths(home).Roots(); len(got) != 3 || got[0] != c.Root || got[1] != filepath.Dir(first.Path) {
		t.Errorf("Roots = %v", got)
	}
	if dst := c.Destinations(c.Paths(home)); len(dst) != 8 {
		t.Errorf("Destinations = %d, want every files, links and templates entry of all three files", len(dst))
	}
}

func TestParseLayeredWithoutLayersIsParseFor(t *testing.T) {
	root := writeConfig(t, "files: [{src: a, dst: ~/.a}]\n")
	c, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Layers) != 0 || c.origins != nil {
		t.Errorf("layers %v, origins %v", c.Layers, c.origins)
	}
}

func TestParseLayeredRejects(t *testing.T) {
	const rootBody = "files: [{src: a, dst: ~/.a}]\ntemplates: {vars: {theme: dark}}\nsystem: {sshKey: {path: ~/.ssh/k}}\nlaunchd:\n  - {label: com.example.job, program: [/bin/true], schedule: {every: 1h}}\ndefaults: [{domain: d, key: k, value: 1}]\n"
	tests := []struct {
		name   string
		layers []string
		want   []string
	}{
		{"protect is root-only", []string{"protect: {immutable: true}\n"}, []string{`layer "l0" is invalid`, "zakwas layer remove l0", "protect is set in a layer"}},
		{"an empty protect is still set", []string{"protect: {immutable: false}\n"}, []string{"protect is set in a layer"}},
		{"a bare agents key is still set", []string{"agents:\nfiles: [{src: x, dst: ~/.x}]\n"}, []string{"agents is set in a layer"}},
		{"an empty brew is still set", []string{"brew:\n"}, []string{"brew is set in a layer"}},
		{"brew is root-only", []string{"brew: {file: Brewfile}\n"}, []string{"brew is set in a layer"}},
		{"mise is root-only", []string{"mise: {config: m.toml}\n"}, []string{"mise is set in a layer"}},
		{"agents is root-only", []string{"agents: {}\n"}, []string{"agents is set in a layer"}},
		{"every root-only section is named", []string{"brew: {file: B}\nmise: {config: m}\n"}, []string{"brew is set in a layer", "mise is set in a layer"}},
		{"a layer that does not validate", []string{"files: [{src: a}]\n"}, []string{`layer "l0" is invalid`, "src and dst are required"}},
		{"unknown fields", []string{"nope: 1\n"}, []string{`layer "l0" is invalid`, "unknown field"}},
		{"a destination in two layers", []string{"files: [{src: x, dst: ~/.same}]\n", "links: [{src: y, dst: ~/.same}]\n"}, []string{"destination", "~/.same", "overlaps", "links[0] of", "files[0] of"}},
		{"a destination in the main config and a layer", []string{"files: [{src: x, dst: ~/.a}]\n"}, []string{"destination", "overlaps", "files[0] of"}},
		{"a layer under a destination", []string{"files: [{src: x, dst: ~/.a/inner}]\n"}, []string{"overlaps"}},
		{"a template var in two files", []string{"templates: {vars: {theme: light}}\n"}, []string{"templates.vars.theme is set in both"}},
		{"two ssh keys", []string{"system: {sshKey: {path: ~/.ssh/other}}\n"}, []string{"system.sshKey is set in both"}},
		{"a launchd label in two files", []string{"launchd:\n  - {label: com.example.job, program: [/bin/true], schedule: {every: 1h}}\n"}, []string{`label "com.example.job" is already used by launchd[0] of`}},
		{"a default set twice", []string{"defaults: [{domain: d, key: k, value: 2}]\n"}, []string{"d k is already set by defaults[0] of"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeConfig(t, rootBody)
			var layers []Layer
			for i, body := range tt.layers {
				l := writeLayer(t, "l"+string(rune('0'+i)), body)
				layers = append(layers, l)
			}
			_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, layers)
			if err == nil {
				t.Fatal("merged")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestParseLayeredNamesBothFiles(t *testing.T) {
	root := writeConfig(t, "files: [{src: a, dst: ~/.same}]\n")
	one := writeLayer(t, "one", "links: [{src: b, dst: ~/.same}]\n")
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{one})
	if err == nil {
		t.Fatal("merged")
	}
	for _, f := range []string{root, one.Path} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error does not name %s:\n%v", f, err)
		}
	}
}

func TestParseLayeredMissingLayer(t *testing.T) {
	root := writeConfig(t, "files: [{src: a, dst: ~/.a}]\n")
	gone := Layer{Name: "work", Path: filepath.Join(t.TempDir(), "gone", FileName)}
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{gone})
	var le *LayerError
	if !errors.As(err, &le) || !le.Missing || le.Layer != gone {
		t.Fatalf("err = %v, want a missing LayerError for %v", err, gone)
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") || !strings.Contains(msg, `layer "work"`) || !strings.Contains(msg, "zakwas layer remove work") || !strings.Contains(msg, gone.Path) {
		t.Errorf("message = %q, want one line naming the layer, its file and the remove command", msg)
	}
	if strings.Contains(le.Detail(), "zakwas layer remove") {
		t.Errorf("Detail() = %q, want no remove hint", le.Detail())
	}
}

func TestParseLayeredRejectsTheSameFileTwice(t *testing.T) {
	root := writeConfig(t, "files: [{src: a, dst: ~/.a}]\n")
	one := writeLayer(t, "one", "files: [{src: b, dst: ~/.b}]\n")
	two := Layer{Name: "two", Path: one.Path}
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{one, two})
	if err == nil || !strings.Contains(err.Error(), "already part of this config") {
		t.Fatalf("err = %v", err)
	}
	rootAsLayer := Layer{Name: "main", Path: filepath.Join(mustRealDir(t, root), FileName)}
	_, err = ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{rootAsLayer})
	if err == nil || !strings.Contains(err.Error(), "already part of this config") {
		t.Fatalf("the main config as a layer: err = %v", err)
	}
}

func TestParseLayeredKeepsMainConfigErrorsFirst(t *testing.T) {
	root := writeConfig(t, "files: [{src: a}]\n")
	one := writeLayer(t, "one", "files: [{src: b, dst: ~/.b}]\n")
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{one})
	if err == nil || !strings.Contains(err.Error(), "files[0]: src and dst are required") || strings.Contains(err.Error(), "of "+one.Path) {
		t.Errorf("err = %v", err)
	}
}

func TestValidLayerName(t *testing.T) {
	for name, want := range map[string]bool{
		"work": true, "work-2": true, "2": true, "a-b-c": true,
		"": false, "Work": false, "-work": false, "work_2": false, "a/b": false, "a b": false, "..": false,
	} {
		if got := ValidLayerName(name); got != want {
			t.Errorf("ValidLayerName(%q) = %v, want %v", name, got, want)
		}
	}
}

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustRealDir(t *testing.T, file string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
