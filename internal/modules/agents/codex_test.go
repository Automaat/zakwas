package agents

import (
	"context"
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
	codexMarketList = "codex plugin marketplace list --json"
	codexPluginList = "codex plugin list --available --json"
)

func newCodexModule(t *testing.T, a config.Agents) (*Module, *runnertest.Fake) {
	t.Helper()
	a.Providers = []string{config.ProviderCodex}
	m, fake := newModule(t, a)
	m.CodexHome = filepath.Join(t.TempDir(), "codex")
	return m, fake
}

func (m *Module) codex() *codex { return m.codexBackend() }

func assertCodexCalls(t *testing.T, m *Module, fake *runnertest.Fake) {
	t.Helper()
	want := []string{"CODEX_HOME=" + m.CodexHome}
	for _, c := range fake.Calls {
		if c.Name != "codex" || c.Dir != "/" || !reflect.DeepEqual(c.Env, want) {
			t.Errorf("%s: dir %q env %v, want codex from / with %v", c, c.Dir, c.Env, want)
		}
	}
}

func assertCodexReadOnly(t *testing.T, fake *runnertest.Fake) {
	t.Helper()
	for _, l := range fake.Lines() {
		if l != codexMarketList && l != codexPluginList {
			t.Errorf("plan ran %q", l)
		}
	}
}

func TestCodexConvergeFromScratch(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{
			"sai":   {Source: "o/sai#v1"},
			"local": {Source: "./mp"},
		},
		Plugins: []config.Plugin{{ID: "humanize@sai"}},
	})
	localDir := filepath.Join(m.Paths.Root, "mp")
	fake.OnOK(codexMarketList, `{"marketplaces": []}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)

	changes := plan(t, m)
	want := []string{
		"+ codex marketplace local (" + localDir + ")",
		"+ codex marketplace sai (o/sai#v1)",
		"+ codex plugin humanize@sai",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	assertCodexReadOnly(t, fake)

	fake.OnOK("codex plugin marketplace add "+localDir+" --json", `{"marketplaceName":"local","installedRoot":"`+localDir+`","alreadyAdded":false}`)
	fake.OnOK("codex plugin marketplace add o/sai --ref v1 --json", `{"marketplaceName":"sai","installedRoot":"/x","alreadyAdded":false}`)
	fake.OnOK("codex plugin add humanize@sai --json", `{"pluginId":"humanize@sai","version":"2.5.0"}`)
	apply(t, changes)
	assertCodexCalls(t, m, fake)

	writeFile(t, m.codex().configPath(), `model = "o3"

[marketplaces.sai]
source_type = "git"
source = "https://github.com/O/sai.git"
ref = "v1"

[marketplaces.local]
source_type = "local"
source = "`+localDir+`"

[plugins."humanize@sai"]
enabled = true
`)
	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(codexMarketList, `{"marketplaces": [{"name":"sai","root":"/x","marketplaceSource":{"sourceType":"git","source":"https://github.com/O/sai.git"}},{"name":"local","root":"`+localDir+`","marketplaceSource":{"sourceType":"local","source":"`+localDir+`"}}]}`)
	fake.OnOK(codexPluginList, `{"installed": [{"pluginId":"humanize@sai","version":"2.5.0","installed":true,"enabled":true}], "available": []}`)
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan not empty: %v", targets(again))
	}
}

func TestCodexEnableAndUpgrade(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{"name":"sai","plugins":[{"name":"a","version":"9.0.0","source":"./a"}]}`)
	writeFile(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"), `{"name":"sai","plugins":[
		{"name":"a","version":"1.1.0","source":{"source":"local","path":"./a"}},
		{"name":"b","source":"./b"},
		{"name":"c","version":"2.0.0","source":"./c"},
		{"name":"d","version":"3.0.0","source":{"source":"git","url":"https://example.com/d.git"}},
		{"name":"e","version":"1.0.0","source":"./e"}]}`)
	writeFile(t, filepath.Join(root, "b", ".codex-plugin", "plugin.json"), `{"version":"3.0.0"}`)
	writeFile(t, filepath.Join(root, "b", ".claude-plugin", "plugin.json"), `{"version":"9.9.9"}`)
	writeFile(t, filepath.Join(root, "c", ".claude-plugin", "plugin.json"), `{"version":"1.5.0"}`)
	for _, upgrade := range []bool{false, true} {
		m, fake := newCodexModule(t, config.Agents{
			Upgrade:      upgrade,
			Marketplaces: map[string]config.Marketplace{"sai": {Source: root}},
			Plugins:      []config.Plugin{{ID: "a@sai"}, {ID: "b@sai"}, {ID: "c@sai"}, {ID: "d@sai"}, {ID: "e@sai"}},
		})
		writeFile(t, m.codex().configPath(), "[marketplaces.sai]\nsource_type = \"local\"\nsource = \""+root+"\"\n")
		fake.OnOK(codexMarketList, `{"marketplaces": [{"name":"sai","root":"`+root+`"}]}`)
		fake.OnOK(codexPluginList, `{"installed": [
			{"pluginId":"a@sai","version":"1.0.0","enabled":false},
			{"pluginId":"b@sai","version":"2.0.0","enabled":true},
			{"pluginId":"c@sai","version":"1.5.0","enabled":true},
			{"pluginId":"d@sai","version":"1.0.0","enabled":true},
			{"pluginId":"e@sai","version":"1.0.0","enabled":false}], "available": []}`)

		want := []string{"~ codex plugin a@sai (enable)", "~ codex plugin e@sai (enable)"}
		if upgrade {
			want = []string{"~ codex plugin a@sai (1.0.0 → 1.1.0, enable)", "~ codex plugin b@sai (2.0.0 → 3.0.0)", "~ codex plugin e@sai (enable)"}
		}
		changes := plan(t, m)
		if got := targets(changes); !reflect.DeepEqual(got, want) {
			t.Errorf("upgrade=%v: got %v\nwant %v", upgrade, got, want)
		}
		for _, id := range []string{"a", "b", "e"} {
			fake.OnOK("codex plugin add "+id+"@sai --json", `{"pluginId":"`+id+`@sai"}`)
		}
		apply(t, changes)
		assertCodexCalls(t, m, fake)
	}
}

func TestCodexPrune(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "keep@sai"}},
	})
	writeFile(t, m.codex().configPath(), `[marketplaces.sai]
source_type = "git"
source = "https://github.com/o/sai.git"

[marketplaces.old]
source_type = "local"
source = "/old"
`)
	fake.OnOK(codexMarketList, `{"marketplaces": [
		{"name":"sai","root":"/r/sai"},
		{"name":"old","root":"/old"},
		{"name":"personal","root":"/home/.agents/plugins","marketplaceSource":null}]}`)
	fake.OnOK(codexPluginList, `{"installed": [
		{"pluginId":"keep@sai","version":"1","enabled":true},
		{"pluginId":"stale@sai","version":"1","enabled":true},
		{"pluginId":"gone@old","version":"2","enabled":false},
		{"pluginId":"mine@personal","version":"1","enabled":true},
		{"pluginId":"tool@openai-curated","version":"1","enabled":true}], "available": []}`)

	changes := plan(t, m)
	want := []string{
		"- codex plugin gone@old (2)",
		"- codex plugin stale@sai (1)",
		"- codex marketplace old (/old)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for _, c := range changes {
		if !c.Destructive {
			t.Errorf("%s: prune must be destructive", c)
		}
	}
	assertCodexReadOnly(t, fake)
	fake.OnOK("codex plugin remove gone@old --json", `{"pluginId":"gone@old"}`)
	fake.OnOK("codex plugin remove stale@sai --json", `{"pluginId":"stale@sai"}`)
	fake.OnOK("codex plugin marketplace remove old --json", `{"marketplaceName":"old"}`)
	apply(t, changes)
	assertCodexCalls(t, m, fake)

	m.Agents.Prune = false
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("prune off: got %v", targets(got))
	}
}

func TestCodexPlanErrors(t *testing.T) {
	base := config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "nope@sai"}},
	}
	saiListed := `{"marketplaces": [{"name":"sai","root":"/r"}]}`
	for _, tc := range []struct {
		name, config, markets, want string
		missing                     bool
	}{
		{name: "codex missing", missing: true, want: "Codex is not installed"},
		{name: "source mismatch", config: "[marketplaces.sai]\nsource = \"https://github.com/other/sai.git\"\n", markets: saiListed, want: `marketplace "sai" comes from https://github.com/other/sai.git`},
		{name: "ref mismatch", config: "[marketplaces.sai]\nsource = \"o/sai\"\nref = \"v2\"\n", markets: saiListed, want: `comes from o/sai#v2`},
		{name: "name taken outside config", markets: saiListed, want: `named "sai" is already loaded from /r`},
		{name: "unknown plugin", config: "[marketplaces.sai]\nsource = \"git@github.com:o/sai.git\"\n", markets: saiListed, want: `plugin "nope" is not in marketplace "sai"`},
		{name: "bad config", config: "[marketplaces\n", markets: `{"marketplaces": []}`, want: "config.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := newCodexModule(t, base)
			if tc.missing {
				fake.Missing("codex")
			}
			if tc.config != "" {
				writeFile(t, m.codex().configPath(), tc.config)
			}
			fake.OnOK(codexMarketList, tc.markets)
			fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
			_, err := m.Plan(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A provider that can't plan must not hold back the others.
func TestProviderFailureKeepsOthers(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Providers:    []string{config.ProviderClaude, config.ProviderCodex},
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
	})
	fake.Missing("codex")
	fake.OnOK(marketList, `[]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Codex is not installed") {
		t.Errorf("err = %v", err)
	}
	if got, want := targets(changes), []string{"+ claude marketplace sai (o/sai, autoUpdate on)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Errorf("refresh with codex missing: %v", err)
	}
}

func TestProvidersPlanSeparately(t *testing.T) {
	m, fake := newModule(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Marketplaces: map[string]config.Marketplace{
			"both": {Source: "o/both"},
			"cc":   {Source: "o/cc", Providers: []string{config.ProviderClaude}},
			"cdx":  {Source: "o/cdx", Providers: []string{config.ProviderCodex}},
		},
		Plugins: []config.Plugin{{ID: "p@both", Providers: []string{config.ProviderCodex}}},
	})
	m.CodexHome = t.TempDir()
	fake.OnOK(marketList, `[]`)
	fake.OnOK(pluginList, `{"installed": [], "available": []}`)
	fake.OnOK(codexMarketList, `{"marketplaces": []}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	want := []string{
		"+ claude marketplace both (o/both, autoUpdate on)",
		"+ claude marketplace cc (o/cc, autoUpdate on)",
		"+ codex marketplace both (o/both)",
		"+ codex marketplace cdx (o/cdx)",
		"+ codex plugin p@both",
	}
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestCodexApplyFailures(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}}})
	fake.OnOK(codexMarketList, `{"marketplaces": []}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	changes := plan(t, m)
	fake.OnOK("codex plugin marketplace add o/sai --json", `{"marketplaceName":"saiplugins","alreadyAdded":false}`)
	err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), `named "saiplugins", not "sai"`) {
		t.Errorf("err = %v", err)
	}

	fake = runnertest.New()
	m.Runner = fake
	fake.OnOK(codexMarketList, `{"marketplaces": []}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	changes = plan(t, m)
	fake.On("codex plugin marketplace add o/sai --json", runner.Result{ExitCode: 1, Stderr: "Error: git clone failed"})
	err = enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}})
	if err == nil || !strings.Contains(err.Error(), "git clone failed") {
		t.Errorf("err = %v", err)
	}
}

func TestCodexRefresh(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{
			"sai":   {Source: "O/sai"},
			"local": {Source: "/mp"},
			"new":   {Source: "o/new"},
			"cc":    {Source: "o/cc", Providers: []string{config.ProviderClaude}},
		},
	})
	m.Agents.Providers = []string{config.ProviderClaude, config.ProviderCodex}
	m.Agents.Marketplaces["cc"] = config.Marketplace{Source: "o/cc", Providers: []string{config.ProviderClaude}}
	fake.Missing("claude")
	writeFile(t, m.codex().configPath(), `[marketplaces.sai]
source_type = "git"
source = "https://github.com/o/sai.git"

[marketplaces.local]
source_type = "local"
source = "/mp"

[marketplaces.cc]
source_type = "git"
source = "https://github.com/o/cc.git"
`)
	fake.OnOK("codex plugin marketplace upgrade sai --json", `{"selectedMarketplaces":["sai"],"upgradedRoots":[],"errors":[]}`)
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.Lines(), []string{"codex plugin marketplace upgrade sai --json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	assertCodexCalls(t, m, fake)

	fake = runnertest.New().Missing("claude")
	m.Runner = fake
	fake.On("codex plugin marketplace upgrade sai --json", runner.Result{ExitCode: 1, Stderr: "Failed to upgrade marketplace `sai`: offline"})
	if err := m.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("err = %v", err)
	}

	m.Agents.Marketplaces["sai"] = config.Marketplace{Source: "evil/sai"}
	fake = runnertest.New().Missing("claude")
	m.Runner = fake
	if err := m.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "not refreshing") {
		t.Errorf("err = %v", err)
	}
	if fake.Ran("codex plugin marketplace upgrade") {
		t.Errorf("refreshed a mismatched source: %v", fake.Lines())
	}
}

func TestCodexDefaultHome(t *testing.T) {
	m, fake := newModule(t, config.Agents{Providers: []string{config.ProviderCodex}})
	m.Agents.Prune = true
	c := m.codex()
	if want := filepath.Join(m.Paths.Home, ".codex", "config.toml"); c.configPath() != want {
		t.Errorf("configPath = %q, want %q", c.configPath(), want)
	}
	fake.OnOK(codexMarketList, `{"marketplaces": []}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	plan(t, m)
	for _, call := range fake.Calls {
		if len(call.Env) != 0 {
			t.Errorf("%s: env %v, want none for the default home", call, call.Env)
		}
	}
}

func TestCodexLatestVersion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{"plugins":[
		{"name":"entry","version":"1.0.0","source":"./entry"},
		{"name":"own","version":"1.0.0","source":"./own"},
		{"name":"escape","version":"1.0.0","source":"./../x"},
		{"name":"rootless","version":"1.0.0","source":"rootless"},
		{"name":"remote","version":"1.0.0","source":{"source":"github","repo":"o/r"}}]}`)
	writeFile(t, filepath.Join(root, "own", ".claude-plugin", "plugin.json"), `{"version":"2.0.0"}`)
	for plugin, want := range map[string]string{
		"entry": "1.0.0", "own": "2.0.0", "escape": "", "rootless": "", "remote": "", "missing": "",
	} {
		if got := codexLatestVersion(root, plugin); got != want {
			t.Errorf("%s: got %q, want %q", plugin, got, want)
		}
	}
	if got := codexLatestVersion("", "entry"); got != "" {
		t.Errorf("no root: got %q", got)
	}
}
