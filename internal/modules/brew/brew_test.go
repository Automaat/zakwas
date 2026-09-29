package brew

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const brewfile = `# comment
tap "automaat/tap", trusted: true
tap "homebrew/services"
brew "jq"
brew "automaat/tap/cache-buster"
cask "ghostty"
mas "Xcode", id: 497799835
`

const cleanupOut = `Would uninstall casks:
zoom
Would uninstall formulae:
wget
Would untap:
old/tap
Would ` + "`brew cleanup`" + `:
Would remove: /Users/x/Library/Caches/Homebrew/foo (1MB)
Run ` + "`brew bundle cleanup --force`" + ` to make these changes.
`

func newModule(t *testing.T, b config.Brew) (*Module, *runnertest.Fake, string) {
	t.Helper()
	return newModuleWith(t, b, brewfile)
}

func newModuleWith(t *testing.T, b config.Brew, content string) (*Module, *runnertest.Fake, string) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "Brewfile")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	b.File = "Brewfile"
	fake := runnertest.New()
	return &Module{Brew: b, Paths: config.Paths{Root: root}, Runner: fake}, fake, file
}

func targets(changes []engine.Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, string(c.Action)+" "+c.Target)
	}
	return out
}

func TestParseBrewfile(t *testing.T) {
	_, _, file := newModule(t, config.Brew{})
	got, err := ParseBrewfile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Kind: "tap", Name: "automaat/tap", Trusted: true},
		{Kind: "tap", Name: "homebrew/services"},
		{Kind: "brew", Name: "jq"},
		{Kind: "brew", Name: "automaat/tap/cache-buster"},
		{Kind: "cask", Name: "ghostty"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestParseCleanup(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"core sections", cleanupOut, []string{"cask zoom", "brew wget", "tap old/tap"}},
		{"empty", "", nil},
		{
			"extension sections",
			"Would uninstall Go packages:\ngolang.org/x/tools/gopls\nWould uninstall VSCode extensions:\nms-python.python\n" +
				"Would uninstall Mac App Store apps:\nXcode\nWould uninstall Shiny things:\nsparkle\n",
			[]string{"go golang.org/x/tools/gopls", "vscode ms-python.python", "mas Xcode", "Shiny things sparkle"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseCleanup(tt.out); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanRequiresTrustedTaps(t *testing.T) {
	m, fake, _ := newModuleWith(t, config.Brew{}, "tap \"homebrew/services\"\ntap \"a/ok\", trusted: true\ntap \"b/untrusted\"\n")

	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), `tap "b/untrusted"`) || strings.Contains(err.Error(), "a/ok") {
		t.Errorf("err = %v, want only b/untrusted reported", err)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("ran %v before rejecting the Brewfile", fake.Lines())
	}
}

func TestOutdatedSkipsPinned(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Upgrade: true})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.OnOK("brew bundle check --file "+file+" --verbose --no-upgrade", "")
	fake.OnOK("brew outdated --json=v2", `{"formulae":[{"name":"jq","pinned":true},{"name":"cache-buster","pinned":false}],"casks":[{"name":"ghostty","pinned":true}]}`)

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"~ brew cache-buster", "! brew bundle install"}) {
		t.Errorf("got %v; pinned entries never upgrade, so they are not drift", got)
	}
}

func TestPlanUpToDate(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Cleanup: config.CleanupZap})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"],"formulae":[]}`)
	fake.OnOK("brew bundle check --file "+file+" --verbose --no-upgrade", "The Brewfile's dependencies are satisfied.\n")
	fake.OnOK("brew bundle cleanup --file "+file, "")

	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes = %v, err = %v", changes, err)
	}
	for _, c := range fake.Calls {
		if !reflect.DeepEqual(c.Env, noAutoUpdate) {
			t.Errorf("%s ran without disabling auto-update", c)
		}
	}
}

func TestPlanInstallUpgradeAndZap(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Cleanup: config.CleanupZap, Upgrade: true})
	fake.OnOK("brew trust --json=v1", `{"taps":[],"formulae":["automaat/tap/cache-buster"]}`)
	fake.OnOK("brew trust --tap automaat/tap", "")
	fake.On("brew bundle check --file "+file+" --verbose --no-upgrade", runner.Result{ExitCode: 1, Stdout: `brew bundle can't satisfy your Brewfile's dependencies.
→ Cask ghostty needs to be installed.
→ Formula jq needs to be installed or updated.
→ Tap new/tap needs to be tapped.
Satisfy missing dependencies with ` + "`brew bundle install`."})
	fake.OnOK("brew outdated --json=v2", `{"formulae":[{"name":"cache-buster"},{"name":"not-in-brewfile"}],"casks":[{"name":"ghostty"}]}`)
	fake.On("brew bundle cleanup --file "+file, runner.Result{ExitCode: 1, Stdout: cleanupOut})
	fake.OnOK("brew bundle install --file "+file, "")
	fake.OnOK("brew bundle cleanup --force --file "+file+" --zap", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"+ trust tap automaat/tap",
		"+ cask ghostty", "+ formula jq", "+ tap new/tap",
		"~ brew cache-buster", "~ cask ghostty",
		"! brew bundle install",
		"- cask zoom", "- brew wget", "- tap old/tap",
		"! brew bundle cleanup",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}

	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "brew", Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	lines := fake.Lines()
	if got := lines[len(lines)-3:]; !reflect.DeepEqual(got, []string{
		"brew trust --tap automaat/tap",
		"brew bundle install --file " + file,
		"brew bundle cleanup --force --file " + file + " --zap",
	}) {
		t.Errorf("applied %v", got)
	}
	for _, c := range fake.Calls {
		if !reflect.DeepEqual(c.Env, noAutoUpdate) {
			t.Errorf("%s ran without disabling auto-update", c)
		}
	}
}

func TestNoUpgradeAndNoCleanup(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Cleanup: config.CleanupNone})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.On("brew bundle check --file "+file+" --verbose --no-upgrade", runner.Result{ExitCode: 1, Stdout: "→ Formula jq needs to be installed.\n"})
	fake.OnOK("brew bundle install --file "+file+" --no-upgrade", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"+ formula jq", "! brew bundle install"}) {
		t.Fatalf("got %v", got)
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if fake.Ran("brew outdated") || fake.Ran("brew bundle cleanup") {
		t.Errorf("unexpected calls: %v", fake.Lines())
	}
}

func TestCheckFailureWithoutMissingEntries(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.On("brew bundle check --file "+file+" --verbose --no-upgrade", runner.Result{ExitCode: 1, Stderr: "Error: invalid Brewfile"})

	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid Brewfile") {
		t.Errorf("err = %v", err)
	}
}

func TestCleanupFailureWithoutListIsAnError(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Cleanup: config.CleanupZap})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.OnOK("brew bundle check --file "+file+" --verbose --no-upgrade", "")
	fake.On("brew bundle cleanup --file "+file, runner.Result{ExitCode: 1, Stderr: "Error: No Brewfile found"})

	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "No Brewfile found") {
		t.Errorf("err = %v", err)
	}
}

func TestBootstrapInstallsHomebrewThenBundle(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Cleanup: config.CleanupZap})
	fake.Missing("brew")
	fake.OnOK("/bin/bash -c "+homebrewInstall, "")
	fake.OnOK("brew bundle install --file "+file+" --no-upgrade", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"+ Homebrew", "+ tap automaat/tap", "+ tap homebrew/services", "+ brew jq", "+ brew automaat/tap/cache-buster",
		"+ cask ghostty", "! brew bundle install",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("planning without brew ran %v", fake.Lines())
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Lines(); len(got) != 2 || !strings.HasPrefix(got[0], "/bin/bash") {
		t.Errorf("ran %v, want the installer then brew bundle install", got)
	}
}

func TestBootstrapStillRequiresTrustedTaps(t *testing.T) {
	m, fake, _ := newModuleWith(t, config.Brew{}, "tap \"x/y\"\n")
	fake.Missing("brew")
	if _, err := m.Plan(context.Background()); err == nil {
		t.Fatal("untrusted tap accepted")
	}
}

func TestOutdatedShowsVersions(t *testing.T) {
	m, fake, file := newModule(t, config.Brew{Upgrade: true})
	fake.OnOK("brew trust --json=v1", `{"taps":["automaat/tap"]}`)
	fake.OnOK("brew bundle check --file "+file+" --verbose --no-upgrade", "")
	fake.OnOK("brew outdated --json=v2", `{"formulae":[{"name":"jq","installed_versions":["1.7.1"],"current_version":"1.8.2"}],"casks":[]}`)

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := changes[0].String(); got != "~ brew jq (1.7.1 → 1.8.2)" {
		t.Errorf("got %q", got)
	}
}

func TestTrustTaps(t *testing.T) {
	in := strings.Join([]string{
		`tap "homebrew/bundle"`,
		`tap "acme/tools"`,
		`tap "acme/other", "https://example.com/other.git"`,
		`tap "acme/ok", trusted: true`,
		`tap "acme/partial", trusted: { formula: ["x"] }`,
		`tap "acme/commented"  # my tap`,
		`tap "acme/hash", "https://example.com/#frag" # note`,
		`brew "jq"`,
		`# tap "acme/disabled"`,
		`cask "firefox"`,
	}, "\n")
	want := strings.Join([]string{
		`tap "homebrew/bundle"`,
		`tap "acme/tools", trusted: true`,
		`tap "acme/other", "https://example.com/other.git", trusted: true`,
		`tap "acme/ok", trusted: true`,
		`tap "acme/partial", trusted: { formula: ["x"] }`,
		`tap "acme/commented", trusted: true  # my tap`,
		`tap "acme/hash", "https://example.com/#frag", trusted: true # note`,
		`brew "jq"`,
		`# tap "acme/disabled"`,
		`cask "firefox"`,
	}, "\n")
	got, changed := TrustTaps([]byte(in))
	if string(got) != want {
		t.Errorf("TrustTaps =\n%s\nwant\n%s", got, want)
	}
	if !reflect.DeepEqual(changed, []string{"acme/tools", "acme/other", "acme/commented", "acme/hash"}) {
		t.Errorf("changed = %v", changed)
	}
}

func TestParseBrewfileIgnoresTrustInComments(t *testing.T) {
	file := filepath.Join(t.TempDir(), "Brewfile")
	body := "tap \"acme/a\" # , trusted: true\ntap \"acme/b\", trusted: true # ok\ntap \"acme/c\", \"https://x/#a\", trusted: true\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := ParseBrewfile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"tap", "acme/a", false}, {"tap", "acme/b", true}, {"tap", "acme/c", true}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

func TestHomebrewInstallerIsPinned(t *testing.T) {
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(installerCommit) {
		t.Fatalf("installerCommit = %q, want a full commit SHA", installerCommit)
	}
	url := "https://raw.githubusercontent.com/Homebrew/install/" + installerCommit + "/install.sh"
	if !strings.Contains(homebrewInstall, url) || strings.Contains(homebrewInstall, "/HEAD/") {
		t.Errorf("homebrewInstall = %q, want it to fetch %s", homebrewInstall, url)
	}
}
