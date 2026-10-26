package proposal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/platform"
)

const layerYAML = `brew: {file: Brewfile}
mise: {config: mise.toml}
agents:
  marketplaces:
    corp: owner/corp
  plugins:
    - ticket@corp
  permissions:
    allow:
      - make test
`

// layeredRepo is the base repo with a "work" layer registered: its own
// zakwas.yaml, Brewfile and mise config, in a directory of its own.
func layeredRepo(t *testing.T, layer string) (cfg *config.Config, home, layerDir string) {
	t.Helper()
	base, home := newRepo(t, baseConfig)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"zakwas.yaml": layer,
		"Brewfile":    "brew \"kubectl\"\n",
		"mise.toml":   "[tools]\nkubectl = \"1.34.1\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err = config.LoadLayered(base.Path, home, platform.Darwin, []config.Layer{{Name: "work", Path: filepath.Join(dir, "zakwas.yaml")}})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, home, dir
}

func TestApplyAddsToTheNamedLayerOnly(t *testing.T) {
	cfg, home, dir := layeredRepo(t, layerYAML)

	res, err := Apply([]Op{
		{Kind: AddPlugin, Name: "second@corp", Layer: "work"},
		{Kind: AddPermission, Decision: "deny", Command: "dd", Layer: "work"},
	}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}

	if res.Layer != "work" || res.Root != dir {
		t.Errorf("layer = %q, root = %q, want work in %s", res.Layer, res.Root, dir)
	}
	if len(res.Edits) != 1 || res.Edits[0].Rel != "zakwas.yaml" || res.Edits[0].Path != filepath.Join(dir, "zakwas.yaml") {
		t.Fatalf("edits = %+v, want only the layer's zakwas.yaml", res.Edits)
	}
	got := string(res.Edits[0].New)
	if !strings.Contains(got, "    - ticket@corp\n    - second@corp\n") || !strings.Contains(got, "  permissions:\n    allow:\n      - make test\n    deny:\n      - dd\n") {
		t.Errorf("layer yaml:\n%s", got)
	}
	if n := len(res.Config.Agents.Plugins); n != 3 {
		t.Errorf("merged config has %d plugins, want 3 (main's first and the layer's two)", n)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "zakwas.yaml")); string(onDisk) != layerYAML {
		t.Error("Apply changed the layer's file on disk")
	}
}

func TestApplyAddsToTheMainConfigWithoutALayer(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	res, err := Apply([]Op{{Kind: AddPlugin, Name: "second@market"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}

	if res.Layer != "" || res.Root != cfg.Root || len(res.Edits) != 1 || res.Edits[0].Path != cfg.Path {
		t.Errorf("layer = %q, root = %q, edits = %+v, want only the main zakwas.yaml", res.Layer, res.Root, res.Edits)
	}
}

func TestApplyRemovesFromTheFileThatHoldsTheEntry(t *testing.T) {
	cfg, home, dir := layeredRepo(t, layerYAML)

	res, err := Apply([]Op{{Kind: RemovePermission, Decision: "allow", Command: "make test"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "work" || len(res.Edits) != 1 || res.Edits[0].Path != filepath.Join(dir, "zakwas.yaml") {
		t.Fatalf("layer = %q, edits = %+v, want the work layer's file", res.Layer, res.Edits)
	}

	res, err = Apply([]Op{{Kind: RemovePermission, Decision: "allow", Command: "ls"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "" || res.Edits[0].Path != cfg.Path {
		t.Errorf("layer = %q, edits = %+v, want the main file for a main rule", res.Layer, res.Edits)
	}
}

func TestApplyRefusesAnEntryOfAnotherLayer(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	for _, op := range []Op{
		{Kind: RemovePermission, Decision: "allow", Command: "make test", Layer: "other"},
		{Kind: RemovePermission, Decision: "allow", Command: "ls", Layer: "work"},
	} {
		_, err := Apply([]Op{op}, cfg, home)
		if err == nil || !strings.Contains(err.Error(), "the entry is in") && !strings.Contains(err.Error(), "not registered") {
			t.Errorf("%+v: err = %v, want a refusal naming where the entry is", op, err)
		}
	}
}

func TestApplyRefusesAnUnregisteredLayer(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	_, err := Apply([]Op{{Kind: AddPermission, Decision: "deny", Command: "dd", Layer: "nope"}}, cfg, home)

	if err == nil || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), `layer "nope" is not registered`) {
		t.Errorf("err = %v, want one line naming the layer", err)
	}
}

func TestApplyEditsOneRepoPerProposal(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	_, err := Apply([]Op{
		{Kind: AddPermission, Decision: "deny", Command: "dd", Layer: "work"},
		{Kind: AddPermission, Decision: "deny", Command: "mkfs"},
	}, cfg, home)

	if err == nil || !strings.Contains(err.Error(), "committed in one repo") {
		t.Errorf("err = %v, want a refusal to edit two repos", err)
	}
}

func TestApplyKeepsAMainPluginOffALayerMarketplace(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	_, err := Apply([]Op{{Kind: AddPlugin, Name: "other@corp"}}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), `declared only by layer "work"`) {
		t.Errorf("main plugin from a layer marketplace: err = %v", err)
	}
	if _, err := Apply([]Op{{Kind: AddPlugin, Name: "other@market", Layer: "work"}}, cfg, home); err != nil {
		t.Errorf("layer plugin from a main marketplace: %v", err)
	}
}

func TestApplyAddsBrewPackagesToTheLayerBrewfile(t *testing.T) {
	cfg, home, dir := layeredRepo(t, layerYAML)

	res, err := Apply([]Op{{Kind: AddBrew, Name: "stern", Layer: "work"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edits) != 1 || res.Edits[0].Path != filepath.Join(dir, "Brewfile") || res.Edits[0].Rel != "Brewfile" || string(res.Edits[0].New) != "brew \"kubectl\"\nbrew \"stern\"\n" {
		t.Errorf("edits = %+v", res.Edits)
	}

	for _, op := range []Op{
		{Kind: AddBrew, Name: "git", Layer: "work"},
		{Kind: AddBrew, Name: "kubectl"},
	} {
		if _, err := Apply([]Op{op}, cfg, home); err == nil || !strings.Contains(err.Error(), "already in the Brewfile") {
			t.Errorf("%+v: err = %v, want the package reported as already declared", op, err)
		}
	}
}

func TestApplyNeedsTheLayerToSetTheFileItEdits(t *testing.T) {
	cfg, home, _ := layeredRepo(t, "agents: {}\n")
	cfg.Brew.Layers = nil

	_, err := Apply([]Op{{Kind: AddBrew, Name: "stern", Layer: "work"}}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), `layer "work" has no brew.file`) {
		t.Errorf("brew: err = %v", err)
	}
	_, err = Apply([]Op{{Kind: PinMise, Name: "stern", Version: "1", Layer: "work"}}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), `layer "work" has no mise.config`) {
		t.Errorf("mise: err = %v", err)
	}
}

func TestApplyPinsMiseToolsInTheFileThatHoldsThem(t *testing.T) {
	cfg, home, dir := layeredRepo(t, layerYAML)

	res, err := Apply([]Op{{Kind: PinMise, Name: "kubectl", Version: "1.35.0"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "work" || len(res.Edits) != 1 || res.Edits[0].Path != filepath.Join(dir, "mise.toml") || string(res.Edits[0].New) != "[tools]\nkubectl = \"1.35.0\"\n" {
		t.Errorf("layer = %q, edits = %+v, want the bump in the layer's mise config", res.Layer, res.Edits)
	}

	res, err = Apply([]Op{{Kind: PinMise, Name: "go", Version: "1.28"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "" || res.Edits[0].Rel != "mise/config.toml" {
		t.Errorf("layer = %q, edits = %+v, want the main mise config", res.Layer, res.Edits)
	}

	res, err = Apply([]Op{{Kind: PinMise, Name: "stern", Version: "1.0", Layer: "work"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "work" || res.Edits[0].Path != filepath.Join(dir, "mise.toml") {
		t.Errorf("layer = %q, edits = %+v, want a new pin in the layer's config", res.Layer, res.Edits)
	}
}

func TestApplyNeverPinsAToolTwice(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)

	for _, op := range []Op{
		{Kind: PinMise, Name: "go", Version: "1.28", Layer: "work"},
		{Kind: PinMise, Name: "kubectl", Version: "1.0", Layer: "other"},
	} {
		_, err := Apply([]Op{op}, cfg, home)
		if err == nil || !strings.Contains(err.Error(), "mise would pick one of two pins") && !strings.Contains(err.Error(), "not registered") {
			t.Errorf("%+v: err = %v, want a refusal", op, err)
		}
	}
}

func TestApplyRefusesLayerFilesOutsideTheLayerRepo(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(outside, "Brewfile")
	if err := os.WriteFile(shared, []byte("brew \"kubectl\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, home, _ := layeredRepo(t, "brew: {file: "+shared+"}\n")

	_, err = Apply([]Op{{Kind: AddBrew, Name: "stern", Layer: "work"}}, cfg, home)

	if err == nil || !strings.Contains(err.Error(), "is outside the config repo") {
		t.Errorf("err = %v, want a refusal for a Brewfile the layer's repo doesn't hold", err)
	}
}

func TestOpValidateChecksTheLayerName(t *testing.T) {
	if err := (Op{Kind: AddBrew, Name: "jq", Layer: "Work Layer"}).Validate(); err == nil {
		t.Error("expected an invalid layer name to be refused")
	}
	if err := (Op{Kind: AddBrew, Name: "jq", Layer: "work-2"}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestDefaultLayer(t *testing.T) {
	ops := []Op{{Kind: AddBrew, Name: "a"}, {Kind: AddBrew, Name: "b", Layer: "work"}}
	if err := DefaultLayer(ops, "work"); err != nil || ops[0].Layer != "work" {
		t.Errorf("ops = %+v, err = %v", ops, err)
	}
	if err := DefaultLayer([]Op{{Kind: AddBrew, Name: "a", Layer: "lab"}}, "work"); err == nil {
		t.Error("expected an operation naming another layer to be refused")
	}
	if ops := []Op{{Kind: AddBrew, Name: "a"}}; DefaultLayer(ops, "") != nil || ops[0].Layer != "" {
		t.Error("an empty layer must change nothing")
	}
}

func TestApplyRemovesTheRuleOfTheLayerNamedWhenTwoFilesDeclareTheCommand(t *testing.T) {
	mainYAML := strings.Replace(baseConfig, "providers: [claude]", "providers: [claude, opencode]", 1)
	mainYAML = strings.Replace(mainYAML, "    deny:\n      - rm\n", "    deny:\n      - command: rm\n        providers: [claude]\n", 1)
	base, home := newRepo(t, mainYAML)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	layer := config.Layer{Name: "work", Path: filepath.Join(dir, "zakwas.yaml")}
	if err := os.WriteFile(layer.Path, []byte("agents:\n  permissions:\n    deny:\n      - command: rm\n        providers: [opencode]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadLayered(base.Path, home, platform.Darwin, []config.Layer{layer})
	if err != nil {
		t.Fatal(err)
	}

	res, err := Apply([]Op{{Kind: RemovePermission, Decision: "deny", Command: "rm", Layer: "work"}}, cfg, home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Layer != "work" || len(res.Edits) != 1 || res.Edits[0].Path != layer.Path {
		t.Errorf("layer = %q, edits = %+v, want the layer's rule removed", res.Layer, res.Edits)
	}
	_, err = Apply([]Op{{Kind: RemovePermission, Decision: "deny", Command: "rm"}}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), "name a layer") {
		t.Errorf("without a layer: err = %v, want the ambiguity reported", err)
	}
}

func TestApplyRefusesALayerPluginFromAnotherLayersMarketplace(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(other, "zakwas.yaml")
	if err := os.WriteFile(otherFile, []byte("agents:\n  marketplaces:\n    ok: owner/ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.LoadLayered(cfg.Path, home, platform.Darwin, append(cfg.LayerRefs(), config.Layer{Name: "other", Path: otherFile}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = Apply([]Op{{Kind: AddPlugin, Name: "w2@ok", Layer: "work"}}, cfg, home)
	if err == nil || !strings.Contains(err.Error(), `layer "work" can't depend on another layer`) {
		t.Errorf("err = %v", err)
	}
	if _, err := Apply([]Op{{Kind: AddPlugin, Name: "w2@ok", Layer: "other"}}, cfg, home); err != nil {
		t.Errorf("a plugin in the layer that declares the marketplace: %v", err)
	}
}

func TestApplyLeavesTheMainFileAloneForALayerOnlyProposal(t *testing.T) {
	cfg, home, _ := layeredRepo(t, layerYAML)
	real := filepath.Join(t.TempDir(), "real.yaml")
	data, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, cfg.Path); err != nil {
		t.Skip("symlinks unavailable")
	}

	if _, err := Apply([]Op{{Kind: AddPermission, Decision: "deny", Command: "dd", Layer: "work"}}, cfg, home); err != nil {
		t.Errorf("a layer edit with a symlinked main file: %v", err)
	}
	if _, err := Apply([]Op{{Kind: AddPermission, Decision: "deny", Command: "dd"}}, cfg, home); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("a main edit with a symlinked main file: err = %v", err)
	}
}
