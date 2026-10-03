package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const (
	marketList = "claude plugin marketplace list --json"
	pluginList = "claude plugin list --json --available"
)

func newModule(t *testing.T, a config.Agents) (*Module, *runnertest.Fake) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	fake := runnertest.New()
	return &Module{
		Agents:    a,
		Paths:     config.Paths{Home: home, Root: root},
		Runner:    fake,
		ClaudeDir: filepath.Join(home, ".claude"),
	}, fake
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func targets(changes []engine.Change) []string {
	var out []string
	for _, c := range changes {
		s := string(c.Action) + " " + c.Target
		if sum := c.Summary(); sum != "" {
			s += " (" + sum + ")"
		}
		out = append(out, s)
	}
	return out
}

func plan(t *testing.T, m *Module) []engine.Change {
	t.Helper()
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func apply(t *testing.T, changes []engine.Change) {
	t.Helper()
	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}}); err != nil {
		t.Fatal(err)
	}
}

// assertReadOnly fails when plan ran anything but the two list commands.
func assertReadOnly(t *testing.T, fake *runnertest.Fake) {
	t.Helper()
	for _, c := range fake.Calls {
		if l := c.String(); l != marketList && l != pluginList {
			t.Errorf("plan ran %q", l)
		}
	}
}

func assertNoYes(t *testing.T, fake *runnertest.Fake) {
	t.Helper()
	for _, c := range fake.Calls {
		for _, a := range c.Args {
			if a == "-y" || a == "--yes" || a == "--accept-command" {
				t.Errorf("%s: zakwas must never accept marketplace commands", c)
			}
		}
		if c.Dir != "/" {
			t.Errorf("%s: dir %q, want /", c, c.Dir)
		}
	}
}

func knownJSON(entries map[string]bool) string {
	m := map[string]any{}
	for name, auto := range entries {
		e := map[string]any{"source": map[string]string{"source": "github", "repo": "o/" + name}, "installLocation": "/x/" + name}
		if auto {
			e["autoUpdate"] = true
		}
		m[name] = e
	}
	data, _ := json.Marshal(m)
	return string(data)
}

func TestConvergeFromScratch(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{
			"sai":   {Source: "o/sai"},
			"cdx":   {Source: "o/cdx", Providers: []string{config.ProviderCodex}},
			"local": {Source: "./mp"},
		},
		Plugins: []config.Plugin{{ID: "humanize@sai"}, {ID: "x@cdx"}},
	})
	localDir := filepath.Join(m.Paths.Root, "mp")
	fake.OnOK(marketList, "[]")
	fake.OnOK(pluginList, `{"installed": [], "available": [{"pluginId": "humanize@sai", "version": "2.5.0"}]}`)

	changes := plan(t, m)
	want := []string{
		"+ claude marketplace local (" + localDir + ", autoUpdate on)",
		"+ claude marketplace sai (o/sai, autoUpdate on)",
		"+ claude plugin humanize@sai (→ 2.5.0)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	assertReadOnly(t, fake)

	writeFile(t, m.claude().knownPath(), knownJSON(map[string]bool{"sai": false, "local": false}))
	fake.OnOK("claude plugin marketplace add "+localDir+" --scope user --json", `{"command":"marketplace-add","outcome":"ok","marketplace":"local"}`)
	fake.OnOK("claude plugin marketplace add o/sai --scope user --json", `{"command":"marketplace-add","outcome":"ok","marketplace":"sai"}`)
	fake.OnOK("claude plugin install humanize@sai --scope user --json", `{"command":"install","outcome":"ok"}`)
	apply(t, changes)
	assertNoYes(t, fake)

	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai"},{"name":"local","source":"directory","path":"`+localDir+`"}]`)
	fake.OnOK(pluginList, `{"installed": [{"id":"humanize@sai","version":"2.5.0","scope":"user","enabled":true}], "available": []}`)
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan not empty: %v", targets(again))
	}
}

func (m *Module) claude() *claude { return m.backends()[config.ProviderClaude].(*claude) }

func TestEnableAndUpgrade(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		m, fake := newModule(t, config.Agents{
			Upgrade:      upgrade,
			Marketplaces: map[string]config.Marketplace{"sai": {Source: "https://github.com/O/sai.git"}},
			Plugins:      []config.Plugin{{ID: "a@sai"}, {ID: "b@sai"}, {ID: "c@sai"}, {ID: "d@sai"}, {ID: "e@sai"}},
		})
		loc := t.TempDir()
		writeFile(t, filepath.Join(loc, ".claude-plugin", "marketplace.json"), `{"name":"sai","plugins":[
			{"name":"a","version":"1.1.0","source":"./a"},
			{"name":"e","version":"9.0.0","source":"./e"},
			{"name":"b","source":"./b"},
			{"name":"c","source":{"source":"github","repo":"x/c"}},
			{"name":"d","source":"../escape"}]}`)
		writeFile(t, filepath.Join(loc, "b", ".claude-plugin", "plugin.json"), `{"version":"3.0.0"}`)
		writeFile(t, filepath.Join(loc, "e", ".claude-plugin", "plugin.json"), `{"version":"1.0.0"}`)
		writeFile(t, m.claude().knownPath(), knownJSON(map[string]bool{"sai": true}))
		fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai","installLocation":"`+loc+`"}]`)
		fake.OnOK(pluginList, `{"installed": [
			{"id":"a@sai","version":"1.0.0","scope":"user","enabled":false},
			{"id":"b@sai","version":"2.0.0","scope":"user","enabled":true},
			{"id":"c@sai","version":"abc123","scope":"user","enabled":true},
			{"id":"d@sai","version":"1.0.0","scope":"user","enabled":true},
			{"id":"e@sai","version":"1.0.0","scope":"user","enabled":true}], "available": []}`)

		want := []string{"~ claude plugin a@sai (enable)"}
		if upgrade {
			want = append(want, "~ claude plugin a@sai (1.0.0 → 1.1.0)", "~ claude plugin b@sai (2.0.0 → 3.0.0)")
		}
		changes := plan(t, m)
		if got := targets(changes); !reflect.DeepEqual(got, want) {
			t.Errorf("upgrade=%v: got %v\nwant %v", upgrade, got, want)
		}
		fake.OnOK("claude plugin enable a@sai --scope user --json", `{"outcome":"ok"}`)
		fake.OnOK("claude plugin update a@sai --scope user --json", `{"outcome":"ok"}`)
		fake.OnOK("claude plugin update b@sai --scope user --json", `{"outcome":"ok"}`)
		apply(t, changes)
		assertNoYes(t, fake)
	}
}

func TestPrune(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "keep@sai"}},
	})
	writeFile(t, m.claude().knownPath(), knownJSON(map[string]bool{"sai": true, "old": false, "proj": false}))
	fake.OnOK(marketList, `[
		{"name":"sai","source":"github","repo":"o/sai"},
		{"name":"old","source":"git","url":"https://example.com/old.git"},
		{"name":"proj","source":"github","repo":"o/proj"}]`)
	fake.OnOK(pluginList, `{"installed": [
		{"id":"keep@sai","version":"1","scope":"user","enabled":true},
		{"id":"stale@sai","version":"1","scope":"user","enabled":true},
		{"id":"gone@old","version":"2","scope":"user","enabled":false},
		{"id":"team@proj","version":"1","scope":"project","enabled":true},
		{"id":"mine@sai","version":"1","scope":"local","enabled":true},
		{"id":"tool@skills-dir","version":"1","scope":"user","enabled":true}], "available": []}`)

	changes := plan(t, m)
	want := []string{
		"- claude plugin gone@old (2)",
		"- claude plugin stale@sai (1)",
		"- claude marketplace old (https://example.com/old.git)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, c := range changes {
		if !c.Destructive {
			t.Errorf("%s: prune must be destructive", c)
		}
	}
	fake.OnOK("claude plugin uninstall gone@old --scope user --json", `{"outcome":"ok"}`)
	fake.OnOK("claude plugin uninstall stale@sai --scope user --json", `{"outcome":"ok"}`)
	fake.OnOK("claude plugin marketplace remove old --scope user --json", `{"outcome":"ok"}`)
	apply(t, changes)
	assertNoYes(t, fake)

	m.Agents.Providers = []string{config.ProviderCodex}
	m.Agents.Marketplaces["sai"] = config.Marketplace{Source: "o/sai"}
	fake = runnertest.New()
	m.Runner = fake
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("claude not managed: got %v", targets(got))
	}
	if len(fake.Calls) != 0 {
		t.Errorf("unmanaged provider must not be queried: %v", fake.Lines())
	}
}

func TestPlanErrors(t *testing.T) {
	base := config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "nope@sai"}},
	}
	for _, tc := range []struct {
		name, markets, want string
		missing             bool
	}{
		{name: "claude missing", missing: true, want: "Claude Code is not installed"},
		{name: "source mismatch", markets: `[{"name":"sai","source":"github","repo":"other/sai"}]`, want: `marketplace "sai" comes from other/sai`},
		{name: "unknown plugin", markets: `[{"name":"sai","source":"git","url":"git@github.com:o/sai.git"}]`, want: `plugin "nope" is not in marketplace "sai"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := newModule(t, base)
			if tc.missing {
				fake.Missing("claude")
			}
			fake.OnOK(marketList, tc.markets)
			fake.OnOK(pluginList, `{"installed": [], "available": []}`)
			_, err := m.Plan(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestApplyFailures(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "cmd@sai"}},
	})
	fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai"}]`)
	fake.OnOK(pluginList, `{"installed": [], "available": [{"pluginId":"cmd@sai"}]}`)
	writeFile(t, m.claude().knownPath(), knownJSON(map[string]bool{"sai": true}))
	changes := plan(t, m)
	fake.On("claude plugin install cmd@sai --scope user --json", runner.Result{
		ExitCode: 1,
		Stdout:   "Installing…\n" + `{"command":"install","outcome":"failed","shownCommand":{"command":"curl x | sh","sha256":"ab"}}`,
	})
	err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), "never accepts") || !strings.Contains(err.Error(), "curl x | sh") {
		t.Errorf("err = %v", err)
	}
	assertNoYes(t, fake)

	m, fake = newModule(t, config.Agents{Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}}})
	fake.OnOK(marketList, `[]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	changes = plan(t, m)
	fake.OnOK("claude plugin marketplace add o/sai --scope user --json", `{"outcome":"ok","marketplace":"saiplugins"}`)
	err = enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), `named "saiplugins", not "sai"`) {
		t.Errorf("err = %v", err)
	}

	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(marketList, `[]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	changes = plan(t, m)
	fake.On("claude plugin marketplace add o/sai --scope user --json", runner.Result{
		ExitCode: 1,
		Stdout:   `{"outcome":"failed","message":"Path does not exist"}`,
	})
	err = enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), "Path does not exist") {
		t.Errorf("err = %v", err)
	}
}

func TestAutoUpdate(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"a": {Source: "o/a"}, "b": {Source: "o/b"}},
	})
	c := m.claude()
	writeFile(t, c.knownPath(), knownJSON(map[string]bool{"a": true, "b": false}))
	settings := `{
  "model": "opus",
  "enabledPlugins": {"x@a": true},
  "extraKnownMarketplaces": {
    "a": {"source": {"source": "github", "repo": "o/a"}},
    "other": {"source": {"source": "github", "repo": "o/other"}}
  },
  "alwaysThinkingEnabled": true
}`
	real := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, real, settings)
	if err := os.MkdirAll(filepath.Dir(c.settingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, c.settingsPath()); err != nil {
		t.Fatal(err)
	}
	fake.OnOK(marketList, `[{"name":"a","source":"github","repo":"o/a"},{"name":"b","source":"github","repo":"o/b"}]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)

	changes := plan(t, m)
	want := []string{"~ claude marketplace a (turn on autoUpdate)", "~ claude marketplace b (turn on autoUpdate)"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	before := readFile(t, c.knownPath())
	apply(t, changes)
	if len(fake.Calls) != 2 {
		t.Errorf("autoUpdate must not run claude: %v", fake.Lines())
	}
	if readFile(t, c.knownPath()) == before {
		t.Error("known_marketplaces.json not updated")
	}
	if fi, err := os.Lstat(c.settingsPath()); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("settings symlink replaced: %v", err)
	}
	got := readFile(t, real)
	for _, key := range []string{`"model"`, `"enabledPlugins"`, `"extraKnownMarketplaces"`, `"alwaysThinkingEnabled"`} {
		if !strings.Contains(got, key) {
			t.Fatalf("settings lost %s:\n%s", key, got)
		}
	}
	order := []int{
		strings.Index(got, `"model"`),
		strings.Index(got, `"enabledPlugins"`),
		strings.Index(got, `"extraKnownMarketplaces"`),
		strings.Index(got, `"alwaysThinkingEnabled"`),
	}
	if !slices.IsSorted(order) {
		t.Errorf("settings keys reordered:\n%s", got)
	}
	var parsed struct {
		Extra map[string]struct {
			AutoUpdate *bool `json:"autoUpdate"`
		} `json:"extraKnownMarketplaces"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	if !isTrue(parsed.Extra["a"].AutoUpdate) || parsed.Extra["other"].AutoUpdate != nil {
		t.Errorf("settings autoUpdate = %s", got)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %v", fi.Mode())
	}

	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan not empty: %v", targets(again))
	}

	if err := os.Chmod(real, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := writeObject(c.settingsPath(), object{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only settings: err = %v", err)
	}
}

func TestRefresh(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}, "new": {Source: "o/new"}, "cdx": {Source: "o/cdx", Providers: []string{config.ProviderCodex}}},
	})
	fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai"},{"name":"cdx","source":"github","repo":"o/cdx"}]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK("claude plugin marketplace update sai --json", `{"outcome":"ok"}`)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{marketList, "claude plugin marketplace update sai --json"}
	if got := fake.Lines(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(marketList, `[{"name":"sai","source":"github","repo":"o/sai"}]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.On("claude plugin marketplace update sai --json", runner.Result{ExitCode: 1, Stdout: `{"outcome":"failed","message":"offline"}`})
	if err := m.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("err = %v", err)
	}
}

func TestCanonical(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	same := [][2]string{
		{"Owner/Repo", "owner/repo"},
		{"owner/repo", "https://github.com/owner/repo.git"},
		{"owner/repo", "git@github.com:Owner/repo.git"},
		{"owner/repo", "https://github.com/owner/repo/"},
		{link, dir},
		{"https://example.com/m.git", "https://example.com/m"},
	}
	for _, p := range same {
		if canonical(p[0]) != canonical(p[1]) {
			t.Errorf("%q and %q should match: %q vs %q", p[0], p[1], canonical(p[0]), canonical(p[1]))
		}
	}
	if canonical("owner/repo") == canonical("owner/other") {
		t.Error("different repos match")
	}
}

func TestSource(t *testing.T) {
	m, _ := newModule(t, config.Agents{})
	for in, want := range map[string]string{
		"o/r":           "o/r",
		"./mp":          filepath.Join(m.Paths.Root, "mp"),
		"../mp":         filepath.Join(filepath.Dir(m.Paths.Root), "mp"),
		"~/mp":          filepath.Join(m.Paths.Home, "mp"),
		"/abs/mp":       "/abs/mp",
		"git@x.com:a/b": "git@x.com:a/b",
	} {
		if got := m.source(in); got != want {
			t.Errorf("source(%q) = %q, want %q", in, got, want)
		}
	}
}
