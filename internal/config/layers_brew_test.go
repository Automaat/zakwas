package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/testutil"
)

func TestParseLayeredCollectsLayerBrewfiles(t *testing.T) {
	root := writeConfig(t, "brew: {file: Brewfile, cleanup: zap, upgrade: true}\n")
	work := writeLayer(t, "work", "brew: {file: Brewfile.work}\n")
	plain := writeLayer(t, "plain", "files: [{src: a, dst: ~/.a}]\n")
	abs := testutil.Abs("/abs/Brewfile.lab")
	lab := writeLayer(t, "lab", "brew: {file: '"+abs+"'}\n")

	c, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{work, plain, lab})
	if err != nil {
		t.Fatal(err)
	}
	want := []LayerBrewfile{
		{Layer: "work", File: filepath.Join(filepath.Dir(work.Path), "Brewfile.work")},
		{Layer: "lab", File: abs},
	}
	if !reflect.DeepEqual(c.Brew.Layers, want) {
		t.Errorf("Brew.Layers = %+v, want %+v", c.Brew.Layers, want)
	}
	if c.Brew.File != "Brewfile" || c.Brew.Cleanup != CleanupZap || !c.Brew.Upgrade {
		t.Errorf("main brew section changed: %+v", *c.Brew)
	}
}

func TestParseLayeredDoesNotShareBrewWithTheMainConfig(t *testing.T) {
	root := writeConfig(t, "brew: {file: Brewfile}\n")
	main, err := ParseFor(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin)
	if err != nil {
		t.Fatal(err)
	}
	work := writeLayer(t, "work", "brew: {file: Brewfile.work}\n")
	merged, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{work})
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Brew.Layers) != 0 || len(merged.Brew.Layers) != 1 {
		t.Errorf("main %v, merged %v", main.Brew.Layers, merged.Brew.Layers)
	}
}

func TestParseLayeredBrewNeedsTheMainSection(t *testing.T) {
	root := writeConfig(t, "files: [{src: a, dst: ~/.a}]\n")
	work := writeLayer(t, "work", "brew: {file: Brewfile.work}\n")
	_, err := ParseLayered(mustReadBytes(t, root), root, t.TempDir(), platform.Darwin, []Layer{work})
	if err == nil {
		t.Fatal("a layer Brewfile merged into a config without brew")
	}
}
