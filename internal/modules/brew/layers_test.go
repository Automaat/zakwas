package brew

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const (
	rootBrewfile = "tap \"automaat/tap\", trusted: true\nbrew \"jq\"\ncask \"ghostty\"\n"
	workBrewfile = "tap \"acme/work\", trusted: true\nbrew \"acme/work/vpn\"\ncask \"slack\""
	allBrewfiles = rootBrewfile + workBrewfile + "\n"
)

func layered(t *testing.T, b config.Brew, root, work string) (*Module, *runnertest.Fake, string) {
	t.Helper()
	dir := t.TempDir()
	layerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Brewfile"), []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	workFile := filepath.Join(layerDir, "Brewfile.work")
	if err := os.WriteFile(workFile, []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	b.File = "Brewfile"
	b.Layers = []config.LayerBrewfile{{Layer: "work", File: workFile}}
	fake := runnertest.New()
	return &Module{Brew: b, Paths: config.Paths{Root: dir}, Runner: fake}, fake, workFile
}

func TestLayerBrewfilesConvergeAsOne(t *testing.T) {
	m, fake, _ := layered(t, config.Brew{Cleanup: config.CleanupZap}, rootBrewfile, workBrewfile)
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap","acme/work"]}`)
	fake.OnOK("brew trust --tap acme/work", "")
	fake.On("brew bundle check --file=- --verbose --no-upgrade", runner.Result{ExitCode: 1, Stdout: "→ Formula jq needs to be installed.\n→ Cask Slack needs to be installed.\n"})
	fake.On("brew bundle check --file=- --verbose --no-upgrade", runner.Result{Stdout: "The Brewfile's dependencies are satisfied.\n"})
	fake.OnOK("brew bundle cleanup --file=-", "")
	fake.OnOK("brew bundle install --file=- --no-upgrade", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ trust tap acme/work", "+ formula jq", "+ cask Slack", "! brew bundle install"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "brew", Changes: changes}}); err != nil {
		t.Fatal(err)
	}

	again, err := m.Plan(context.Background())
	if err != nil || len(again) != 0 {
		t.Fatalf("second plan = %v, err = %v; want none", targets(again), err)
	}
	for _, c := range fake.Calls {
		if !reflect.DeepEqual(c.Env, noAutoUpdate) {
			t.Errorf("%s ran without disabling auto-update", c)
		}
		if strings.HasPrefix(c.String(), "brew bundle") && c.Stdin != allBrewfiles {
			t.Errorf("%s got stdin %q, want both Brewfiles %q", c, c.Stdin, allBrewfiles)
		}
	}
}

func TestLayerBrewfilesCleanupKeepsEveryFilesPackages(t *testing.T) {
	m, fake, _ := layered(t, config.Brew{Cleanup: config.CleanupZap}, rootBrewfile, workBrewfile)
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap","acme/work"]}`)
	fake.OnOK("brew bundle check --file=- --verbose --no-upgrade", "")
	fake.On("brew bundle cleanup --file=-", runner.Result{ExitCode: 1, Stdout: "Would uninstall casks:\nzoom\n"})
	fake.OnOK("brew bundle cleanup --force --file=- --zap", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"- cask zoom", "! brew bundle cleanup"}) {
		t.Fatalf("got %v", got)
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "brew", Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	cleanups := 0
	for _, c := range fake.Calls {
		if strings.HasPrefix(c.String(), "brew bundle cleanup") {
			cleanups++
			if c.Stdin != allBrewfiles {
				t.Errorf("%s got stdin %q: cleanup must see every Brewfile", c, c.Stdin)
			}
		}
	}
	if cleanups != 2 {
		t.Errorf("ran %d cleanups, want the dry run and the forced one", cleanups)
	}
}

func TestLayerBrewfileNeedsTrustedTaps(t *testing.T) {
	m, fake, workFile := layered(t, config.Brew{}, rootBrewfile, "tap \"acme/work\"\nbrew \"acme/work/vpn\"\n")

	for _, installed := range []bool{true, false} {
		if !installed {
			fake.Missing("brew")
		}
		_, err := m.Plan(context.Background())
		if err == nil {
			t.Fatalf("brew installed = %v: untrusted layer tap accepted", installed)
		}
		for _, want := range []string{`tap "acme/work"`, "needs `trusted: true`", workFile, `layer "work"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("brew installed = %v: error lacks %q: %v", installed, want, err)
			}
		}
		if strings.Contains(err.Error(), "automaat/tap") {
			t.Errorf("error blames the main Brewfile: %v", err)
		}
	}
	if len(fake.Calls) != 0 {
		t.Errorf("ran %v before rejecting the Brewfile", fake.Lines())
	}
}

func TestMainBrewfileTrustErrorKeepsItsMessageWithLayers(t *testing.T) {
	m, _, _ := layered(t, config.Brew{}, "tap \"b/untrusted\"\n", workBrewfile)
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), `tap "b/untrusted" in the Brewfile needs`) || strings.Contains(err.Error(), "layer") {
		t.Errorf("err = %v", err)
	}
}

func TestLayerBrewfileMissing(t *testing.T) {
	m, fake, workFile := layered(t, config.Brew{}, rootBrewfile, workBrewfile)
	if err := os.Remove(workFile); err != nil {
		t.Fatal(err)
	}
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), `layer "work"`) || !strings.Contains(err.Error(), workFile) {
		t.Errorf("err = %v, want a failed plan naming the layer and its Brewfile", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("ran %v", fake.Lines())
	}
}

func TestDuplicateEntriesAcrossBrewfilesPlanOnce(t *testing.T) {
	m, fake, _ := layered(t, config.Brew{}, rootBrewfile, "tap \"automaat/tap\", trusted: true\nbrew \"jq\"\n")
	fake.OnOK("brew trust --json=v1", `{"taps":[]}`)
	fake.OnOK("brew trust --tap automaat/tap", "")
	fake.On("brew bundle check --file=- --verbose --no-upgrade", runner.Result{ExitCode: 1, Stdout: "→ Formula jq needs to be installed.\n→ Formula jq needs to be installed.\n→ Tap automaat/tap needs to be tapped.\n→ Tap automaat/tap needs to be tapped.\n"})

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ trust tap automaat/tap", "+ formula jq", "+ tap automaat/tap", "! brew bundle install"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestBootstrapListsEveryBrewfile(t *testing.T) {
	m, fake, _ := layered(t, config.Brew{}, rootBrewfile, "brew \"jq\"\ncask \"slack\"\n")
	fake.Missing("brew")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ Homebrew", "+ tap automaat/tap", "+ brew jq", "+ cask ghostty", "+ cask slack", "! brew bundle install"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("planning without brew ran %v", fake.Lines())
	}
}

func TestUpgradeCoversLayerEntries(t *testing.T) {
	m, fake, _ := layered(t, config.Brew{Upgrade: true}, rootBrewfile, workBrewfile)
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap","acme/work"]}`)
	fake.OnOK("brew bundle check --file=- --verbose --no-upgrade", "")
	fake.OnOK("brew outdated --json=v2", `{"formulae":[{"name":"vpn","installed_versions":["1"],"current_version":"2"}],"casks":[{"name":"slack"}]}`)

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"~ brew vpn", "~ cask slack", "! brew bundle install"}) {
		t.Fatalf("got %v", got)
	}
}

func TestWithoutLayersBrewReadsTheFile(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.OnOK("brew bundle check --file "+file+" --verbose --no-upgrade", "")
	if _, err := m.Plan(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Calls {
		if c.Stdin != "" {
			t.Errorf("%s got stdin without layers", c)
		}
	}
}
