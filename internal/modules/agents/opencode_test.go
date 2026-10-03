package agents

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

// cloneRunner answers `git clone` by writing a marketplace into the clone
// target, as git would.
type cloneRunner struct {
	*runnertest.Fake
	repos map[string]func(dir string)
}

func (r *cloneRunner) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "clone" {
		r.Calls = append(r.Calls, c)
		url, dir := c.Args[len(c.Args)-2], c.Args[len(c.Args)-1]
		write, ok := r.repos[url]
		if !ok {
			return runner.Result{ExitCode: 128, Stderr: "fatal: repository '" + url + "' not found"}, nil
		}
		write(dir)
		return runner.Result{}, nil
	}
	return r.Fake.Run(ctx, c)
}

func newOpencode(t *testing.T, a config.Agents) (*Module, *cloneRunner) {
	t.Helper()
	if a.Providers == nil {
		a.Providers = []string{config.ProviderOpencode}
	}
	r := &cloneRunner{Fake: runnertest.New(), repos: map[string]func(string){}}
	return &Module{Agents: a, Paths: config.Paths{Home: t.TempDir(), Root: t.TempDir()}, Runner: r}, r
}

// marketplace writes a marketplace named name with each plugin at
// plugins/<plugin>, shipping the given skills.
func marketplace(t *testing.T, dir, name string, plugins map[string][]string) {
	t.Helper()
	entries := []string{}
	for p, skills := range plugins {
		entries = append(entries, `{"name":"`+p+`","source":"./plugins/`+p+`"}`)
		for _, s := range skills {
			writeFile(t, filepath.Join(dir, "plugins", p, "skills", s, "SKILL.md"), "---\nname: "+s+"\n---\n")
		}
	}
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), `{"name":"`+name+`","plugins":[`+strings.Join(entries, ",")+`]}`)
}

func skillsDir(m *Module) string { return filepath.Join(m.Paths.Home, ".config", "opencode", "skills") }

func assertLink(t *testing.T, link, target string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil || got != target {
		t.Errorf("%s → %q (%v), want %q", link, got, err, target)
	}
}

func assertConverged(t *testing.T, m *Module) {
	t.Helper()
	if again := plan(t, m); len(again) != 0 {
		t.Errorf("second plan not empty: %v", targets(again))
	}
}

func TestOpencodeLocalMarketplace(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	marketplace(t, root, "mp", map[string][]string{"a": {"one", "two"}, "b": {"three"}, "c": {"unused"}})
	writeFile(t, filepath.Join(root, "plugins", "a", "skills", "notaskill", "README.md"), "")

	changes := plan(t, m)
	want := []string{"+ opencode skill one (a@mp)", "+ opencode skill three (b@mp)", "+ opencode skill two (a@mp)"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if len(r.Calls) != 0 {
		t.Errorf("plan ran %v", r.Lines())
	}
	if _, err := os.Lstat(skillsDir(m)); !os.IsNotExist(err) {
		t.Errorf("plan created the skills dir: %v", err)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(skillsDir(m), "one"), filepath.Join(root, "plugins", "a", "skills", "one"))
	assertLink(t, filepath.Join(skillsDir(m), "three"), filepath.Join(root, "plugins", "b", "skills", "three"))
	if entries, _ := os.ReadDir(os.Getenv("OPENCODE_CONFIG_DIR")); len(entries) != 0 {
		t.Errorf("OPENCODE_CONFIG_DIR must be ignored, got %v", entries)
	}
	assertConverged(t, m)
}

func TestOpencodeGitMarketplace(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "Owner/sai#v1"}},
		Plugins:      []config.Plugin{{ID: "humanize@sai"}},
	})
	r.repos["https://github.com/Owner/sai.git"] = func(dir string) {
		marketplace(t, dir, "sai", map[string][]string{"humanize": {"humanize"}})
	}

	changes := plan(t, m)
	want := []string{"+ opencode marketplace sai (fetch Owner/sai#v1)", "+ opencode skills (of humanize@sai once fetched)"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if len(r.Calls) != 0 {
		t.Errorf("plan ran %v", r.Lines())
	}
	apply(t, changes)
	cache := filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "agents", "sai")
	assertLink(t, filepath.Join(skillsDir(m), "humanize"), filepath.Join(cache, "plugins", "humanize", "skills", "humanize"))
	clone := r.Calls[0]
	if got := strings.Join(clone.Args[:6], " "); got != "clone --quiet --depth 1 --branch v1" || clone.Dir != "/" {
		t.Errorf("clone = %v in %q", clone.Args, clone.Dir)
	}
	if entries, _ := os.ReadDir(filepath.Dir(cache)); len(entries) != 1 {
		t.Errorf("cache holds leftovers: %v", entries)
	}
	assertConverged(t, m)

	r.Fake = runnertest.New()
	r.OnOK("git -C "+cache+" fetch --quiet --depth 1 origin v1", "")
	r.OnOK("git -C "+cache+" reset --quiet --hard FETCH_HEAD", "")
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Lines()); got != 2 {
		t.Errorf("refresh ran %v", r.Lines())
	}

	m.Agents.Marketplaces["sai"] = config.Marketplace{Source: "https://example.com/sai.git"}
	r.repos["https://example.com/sai.git"] = func(dir string) {
		marketplace(t, dir, "sai", map[string][]string{"humanize": {"humanize", "extra"}})
	}
	r.Fake = runnertest.New()
	if err := m.Refresh(context.Background()); err != nil || len(r.Lines()) != 0 {
		t.Errorf("refresh of a changed source: %v, ran %v", err, r.Lines())
	}
	changes = plan(t, m)
	want = []string{"~ opencode marketplace sai (fetch https://example.com/sai.git (was Owner/sai#v1))", "+ opencode skills (of humanize@sai once fetched)"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(skillsDir(m), "extra"), filepath.Join(cache, "plugins", "humanize", "skills", "extra"))
	assertConverged(t, m)
}

func TestOpencodeFailedFetchKeepsCache(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "https://example.com/gone.git"}},
		Plugins:      []config.Plugin{{ID: "x@sai"}},
	})
	cache := filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "agents")
	err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: plan(t, m)}})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		t.Errorf("failed clone left %v", entries)
	}
	if len(r.Calls) != 1 {
		t.Errorf("ran %v", r.Lines())
	}
}

func TestOpencodePrune(t *testing.T) {
	for _, prune := range []bool{false, true} {
		m, _ := newOpencode(t, config.Agents{
			Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
			Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}},
		})
		root := filepath.Join(m.Paths.Root, "mp")
		marketplace(t, root, "mp", map[string][]string{"a": {"keep"}, "b": {"drop", "moved"}})
		apply(t, plan(t, m))
		cache := filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "agents", "old")
		writeFile(t, filepath.Join(cache, "x"), "")
		o := m.opencodeBackend()
		if err := o.update(func(st *opencodeState) { st.Marketplaces["old"] = "o/old" }); err != nil {
			t.Fatal(err)
		}
		mine := filepath.Join(skillsDir(m), "mine")
		if err := os.Symlink(t.TempDir(), mine); err != nil {
			t.Fatal(err)
		}
		moved := filepath.Join(skillsDir(m), "moved")
		if err := os.Remove(moved); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(moved, "SKILL.md"), "user's own")

		m.Agents.Prune = prune
		m.Agents.Plugins = []config.Plugin{{ID: "a@mp"}}
		changes := plan(t, m)
		var want []string
		if prune {
			want = []string{"- opencode skill drop (b@mp)", "- opencode skill moved (no longer zakwas's link, forget it)", "- opencode marketplace old (~/.local/share/zakwas/agents/old)"}
		}
		if got := targets(changes); !reflect.DeepEqual(got, want) {
			t.Fatalf("prune=%v: got %v\nwant %v", prune, got, want)
		}
		for _, c := range changes {
			if c.Destructive == strings.Contains(c.Detail, "forget") {
				t.Errorf("%s: destructive=%v", c, c.Destructive)
			}
		}
		apply(t, changes)
		if prune {
			if _, err := os.Lstat(filepath.Join(skillsDir(m), "drop")); !os.IsNotExist(err) {
				t.Errorf("drop not removed: %v", err)
			}
			if _, err := os.Stat(cache); !os.IsNotExist(err) {
				t.Errorf("old cache not removed: %v", err)
			}
		}
		for _, p := range []string{mine, filepath.Join(moved, "SKILL.md"), filepath.Join(skillsDir(m), "keep")} {
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("prune=%v removed %s: %v", prune, p, err)
			}
		}
		assertConverged(t, m)
	}
}

func TestOpencodeKeepsSkillsOfPendingFetch(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "p@sai"}},
	})
	r.repos["https://github.com/o/sai.git"] = func(dir string) { marketplace(t, dir, "sai", map[string][]string{"p": {"s"}}) }
	apply(t, plan(t, m))
	if err := os.RemoveAll(filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "agents", "sai")); err != nil {
		t.Fatal(err)
	}
	changes := plan(t, m)
	want := []string{"+ opencode marketplace sai (fetch o/sai)", "+ opencode skills (of p@sai once fetched)"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	apply(t, changes)
	assertConverged(t, m)
}

func TestOpencodeLeavesUsersMatchingLinkAlone(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	marketplace(t, root, "mp", map[string][]string{"a": {"s"}})
	if err := os.MkdirAll(skillsDir(m), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(skillsDir(m), "s")
	if err := os.Symlink(filepath.Join(root, "plugins", "a", "skills", "s"), link); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, m)
	m.Agents.Plugins = nil
	assertConverged(t, m)
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("user's link removed: %v", err)
	}
}

func TestOpencodePruneNeverDeletesThroughSymlinkedParent(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"one"}})
	apply(t, plan(t, m))
	cfg := filepath.Join(m.Paths.Home, ".config", "opencode")
	moved := filepath.Join(m.Paths.Root, "opencode")
	if err := os.Rename(cfg, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, cfg); err != nil {
		t.Fatal(err)
	}
	m.Agents.Opencode = &config.Opencode{SkillsDir: "~/.agents/skills"}
	m.Agents.Plugins = nil
	changes := plan(t, m)
	if got := targets(changes); !reflect.DeepEqual(got, []string{"- opencode skill ~/.config/opencode/skills/one (no longer zakwas's link, forget it)"}) {
		t.Fatalf("got %v", got)
	}
	apply(t, changes)
	if _, err := os.Lstat(filepath.Join(moved, "skills", "one")); err != nil {
		t.Errorf("deleted through the symlinked parent: %v", err)
	}
}

func TestOpencodeSkillsDir(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
		Opencode:     &config.Opencode{SkillsDir: "~/custom/skills"},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	marketplace(t, root, "mp", map[string][]string{"a": {"s"}})
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, "custom", "skills", "s"), filepath.Join(root, "plugins", "a", "skills", "s"))
}

func TestOpencodeManifests(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"cdx": {Source: "./cdx"}, "pr": {Source: "./pr"}},
		Plugins:      []config.Plugin{{ID: "a@cdx"}, {ID: "b@pr"}, {ID: "r@pr"}},
	})
	cdx, pr := filepath.Join(m.Paths.Root, "cdx"), filepath.Join(m.Paths.Root, "pr")
	writeFile(t, filepath.Join(cdx, ".agents", "plugins", "marketplace.json"), `{"name":"cdx","plugins":[{"name":"a","source":{"source":"local","path":"./plugins/a"}}]}`)
	writeFile(t, filepath.Join(cdx, "plugins", "a", "skills", "ca", "SKILL.md"), "")
	writeFile(t, filepath.Join(pr, ".claude-plugin", "marketplace.json"), `{"name":"pr","metadata":{"pluginRoot":"./plugins"},"plugins":[{"name":"b","source":"b"},{"name":"r","source":"./"}]}`)
	writeFile(t, filepath.Join(pr, "plugins", "b", "skills", "pb", "SKILL.md"), "")
	writeFile(t, filepath.Join(pr, "skills", "root", "SKILL.md"), "")

	want := []string{"+ opencode skill ca (a@cdx)", "+ opencode skill pb (b@pr)", "+ opencode skill root (r@pr)"}
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestOpencodePlanErrors(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, want string
		plugins              []config.Plugin
		setup                func(t *testing.T, m *Module, r *cloneRunner)
	}{
		{
			name: "opencode missing", want: "opencode is not installed",
			setup: func(_ *testing.T, _ *Module, r *cloneRunner) { r.Missing("opencode") },
		},
		{
			name: "git missing", want: "needs git",
			plugins: []config.Plugin{{ID: "x@remote"}},
			setup:   func(_ *testing.T, _ *Module, r *cloneRunner) { r.Missing("git") },
		},
		{
			name: "duplicate skill", want: `skill "same" is shipped by both a@mp and b@mp`,
			plugins: []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}},
		},
		{
			name: "unknown plugin", want: `plugin "nope" is not in`,
			plugins: []config.Plugin{{ID: "nope@mp"}},
		},
		{
			name: "remote plugin source", want: "not stored in the marketplace repo",
			manifest: `{"name":"mp","plugins":[{"name":"a","source":{"source":"github","repo":"x/a"}}]}`,
		},
		{
			name: "escaping source", want: "not a path inside the marketplace repo",
			manifest: `{"name":"mp","plugins":[{"name":"a","source":"../out"}]}`,
		},
		{
			name: "renamed marketplace", want: `is named "other", not "mp"`,
			manifest: `{"name":"other","plugins":[]}`,
		},
		{
			name: "no manifest", want: "has no .claude-plugin/marketplace.json",
			setup: func(t *testing.T, m *Module, _ *cloneRunner) {
				if err := os.RemoveAll(filepath.Join(m.Paths.Root, "mp", ".claude-plugin")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "user's skill in the way", want: "~/.config/opencode/skills/one already exists and zakwas did not install it",
			setup: func(t *testing.T, m *Module, _ *cloneRunner) {
				writeFile(t, filepath.Join(skillsDir(m), "one", "SKILL.md"), "mine")
			},
		},
		{
			name: "symlinked skills dir", want: "is a symlink",
			setup: func(t *testing.T, m *Module, _ *cloneRunner) {
				if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".config"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(m.Paths.Home, ".config", "opencode")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plugins := tc.plugins
			if plugins == nil {
				plugins = []config.Plugin{{ID: "a@mp"}}
			}
			m, r := newOpencode(t, config.Agents{
				Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}, "remote": {Source: "o/remote"}},
				Plugins:      plugins,
			})
			root := filepath.Join(m.Paths.Root, "mp")
			marketplace(t, root, "mp", map[string][]string{"a": {"one", "same"}, "b": {"same"}})
			if tc.manifest != "" {
				writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), tc.manifest)
			}
			if tc.setup != nil {
				tc.setup(t, m, r)
			}
			_, err := m.Plan(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOpencodeImplicitProviderSkippedWhenMissing(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}},
		Plugins:      []config.Plugin{{ID: "p@sai"}},
	})
	m.Agents.Providers = nil
	r.Missing("opencode", "claude")
	r.OnOK(marketList, "[]")
	_, err := m.Plan(context.Background())
	if err == nil || strings.Contains(err.Error(), "opencode") {
		t.Errorf("err = %v, want only claude's error", err)
	}
	m.Agents.Providers = []string{config.ProviderOpencode}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "opencode is not installed") {
		t.Errorf("explicit opencode: err = %v", err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Errorf("refresh: %v", err)
	}
}

func TestOpencodeRefetchConverges(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}, "mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "p@sai"}, {ID: "gone@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"gone": {"old"}, "tool": {"tool"}})
	r.repos["https://github.com/o/sai.git"] = func(dir string) { marketplace(t, dir, "sai", map[string][]string{"p": {"keep", "extra"}}) }
	r.repos["https://github.com/fork/sai.git"] = func(dir string) { marketplace(t, dir, "sai", map[string][]string{"p": {"keep"}}) }
	apply(t, plan(t, m))

	m.Agents.Marketplaces["sai"] = config.Marketplace{Source: "fork/sai"}
	m.Agents.Plugins = []config.Plugin{{ID: "p@sai"}, {ID: "tool@mp"}}
	changes := plan(t, m)
	want := []string{
		"~ opencode marketplace sai (fetch fork/sai (was o/sai))",
		"+ opencode skill tool (tool@mp)",
		"- opencode skill old (gone@mp)",
		"+ opencode skills (of p@sai once fetched)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if (engine.Plan{{Module: "agents", Changes: changes}}).Steps() != 2 {
		t.Errorf("listed changes must be applied by the last step")
	}
	apply(t, changes)
	if _, err := os.Lstat(filepath.Join(skillsDir(m), "old")); !os.IsNotExist(err) {
		t.Errorf("shown removal not applied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(skillsDir(m), "extra")); !os.IsNotExist(err) {
		t.Errorf("link to the skill the fetch took away kept: %v", err)
	}
	assertConverged(t, m)
}

func TestOpencodeConflictAfterFetchLinksNothing(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "o/sai"}, "mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "p@sai"}, {ID: "a@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"x", "y"}})
	r.repos["https://github.com/o/sai.git"] = func(dir string) { marketplace(t, dir, "sai", map[string][]string{"p": {"x"}}) }
	err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: plan(t, m)}})
	if err == nil || !strings.Contains(err.Error(), `skill "x" is shipped by both`) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(skillsDir(m)); len(entries) != 0 {
		t.Errorf("linked %v before the conflict was known", entries)
	}
}

func TestOpencodePluginOnlyInCodexManifest(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "c@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	marketplace(t, root, "mp", map[string][]string{"a": {"sa"}})
	writeFile(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"), `{"name":"mp","plugins":[{"name":"c","source":{"source":"local","path":"./codex/c"}}]}`)
	writeFile(t, filepath.Join(root, "codex", "c", "skills", "sc", "SKILL.md"), "")
	want := []string{"+ opencode skill sa (a@mp)", "+ opencode skill sc (c@mp)"}
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestOpencodeImplicitProviderSkipsRemotePlugins(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "remote@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{"name":"mp","plugins":[{"name":"a","source":"./a"},{"name":"remote","source":{"source":"github","repo":"x/r"}}]}`)
	writeFile(t, filepath.Join(root, "a", "skills", "s", "SKILL.md"), "")
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "not stored in the marketplace repo") {
		t.Errorf("explicit opencode: err = %v", err)
	}
	m.Agents.Providers = nil
	r.Missing("claude", "codex")
	r.OnOK(marketList, "[]")
	changes, err := m.opencodeBackend().plan(context.Background(), m.desired(config.ProviderOpencode))
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"+ opencode skill s (a@mp)"}) {
		t.Errorf("got %v", got)
	}
}

func TestOpencodeSkillNamesDifferingInCase(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"Review"}, "b": {"review"}})
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), `skill "review" is shipped by both a@mp and b@mp`) {
		t.Errorf("err = %v", err)
	}
}

func TestOpencodeFetchesOnlyUsedMarketplaces(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"big": {Source: "o/big"}},
	})
	r.Missing("git")
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("got %v", targets(got))
	}
	if err := m.Refresh(context.Background()); err != nil || len(r.Calls) != 0 {
		t.Errorf("refresh: %v, ran %v", err, r.Lines())
	}
}

func TestOpencodePrunesLinksInOldSkillsDir(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Prune:        true,
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"one"}, "b": {"two"}})
	apply(t, plan(t, m))
	old := skillsDir(m)

	m.Agents.Opencode = &config.Opencode{SkillsDir: "~/.agents/skills"}
	m.Agents.Plugins = []config.Plugin{{ID: "a@mp"}}
	changes := plan(t, m)
	want := []string{
		"+ opencode skill one (a@mp)",
		"- opencode skill ~/.config/opencode/skills/one (a@mp)",
		"- opencode skill ~/.config/opencode/skills/two (b@mp)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	apply(t, changes)
	if entries, _ := os.ReadDir(old); len(entries) != 0 {
		t.Errorf("old skills dir keeps %v", entries)
	}
	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "one"), filepath.Join(m.Paths.Root, "mp", "plugins", "a", "skills", "one"))
	assertConverged(t, m)
}

func TestOpencodeSkillRenamedInCase(t *testing.T) {
	probe := t.TempDir()
	writeFile(t, filepath.Join(probe, "Case"), "")
	if _, err := os.Stat(filepath.Join(probe, "case")); err != nil {
		t.Skip("case-sensitive file system")
	}
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	marketplace(t, root, "mp", map[string][]string{"a": {"Foo"}})
	apply(t, plan(t, m))
	skills := filepath.Join(root, "plugins", "a", "skills")
	if err := os.Rename(filepath.Join(skills, "Foo"), filepath.Join(skills, "foo")); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))
	entries, _ := os.ReadDir(skillsDir(m))
	if len(entries) != 1 || entries[0].Name() != "foo" {
		t.Errorf("skills dir holds %v, want only foo", entries)
	}
	assertConverged(t, m)
}

func TestOpencodeImplicitProviderSkipsConflicts(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
	})
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"mine", "new"}})
	writeFile(t, filepath.Join(skillsDir(m), "mine", "SKILL.md"), "hand-made")
	m.Agents.Providers = nil
	changes, err := m.opencodeBackend().plan(context.Background(), m.desired(config.ProviderOpencode))
	if err != nil {
		t.Fatal(err)
	}
	if got := targets(changes); !reflect.DeepEqual(got, []string{"+ opencode skill new (a@mp)"}) {
		t.Errorf("got %v", got)
	}
}

func TestOpencodeDefaultTargetedPluginsAreBestEffort(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}, "named": {Source: "./named", Providers: []string{config.ProviderOpencode, config.ProviderClaude}}},
		Plugins:      []config.Plugin{{ID: "a@mp"}, {ID: "b@mp"}, {ID: "n@named"}},
	})
	m.Agents.Providers = nil
	r.Missing("claude", "codex")
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"review"}, "b": {"x"}})
	marketplace(t, filepath.Join(m.Paths.Root, "named"), "named", map[string][]string{"n": {"x"}})
	_, err := m.opencodeBackend().plan(context.Background(), m.desired(config.ProviderOpencode))
	if err == nil || !strings.Contains(err.Error(), `skill "x" is shipped by both b@mp and n@named`) {
		t.Errorf("default-targeted duplicate: err = %v", err)
	}

	r.Missing("opencode")
	if _, err := m.opencodeBackend().plan(context.Background(), m.desired(config.ProviderOpencode)); err == nil || !strings.Contains(err.Error(), "opencode is not installed") {
		t.Errorf("marketplace naming opencode: err = %v", err)
	}
	m.Agents.Plugins = []config.Plugin{{ID: "a@mp"}}
	if got, err := m.opencodeBackend().plan(context.Background(), m.desired(config.ProviderOpencode)); err != nil || len(got) != 0 {
		t.Errorf("default-targeted only, opencode missing: %v, %v", targets(got), err)
	}
}

func TestOpencodeDeclaredSkillPaths(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "a@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{"name":"mp","plugins":[{"name":"a","source":"./a","skills":["./inline/one"]}]}`)
	writeFile(t, filepath.Join(root, "a", ".claude-plugin", "plugin.json"), `{"name":"a","skills":"./custom"}`)
	for _, p := range []string{"skills/std", "custom/c1", "custom/c2", "inline/one"} {
		writeFile(t, filepath.Join(root, "a", p, "SKILL.md"), "")
	}
	writeFile(t, filepath.Join(root, "outside", "SKILL.md"), "")
	writeFile(t, filepath.Join(root, "a", ".claude-plugin", "plugin.json"), `{"name":"a","skills":["./custom","../../../escape"]}`)
	want := []string{"+ opencode skill c1 (a@mp)", "+ opencode skill c2 (a@mp)", "+ opencode skill one (a@mp)"}
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestOpencodeDefaultTargetedFetchFailureSkips(t *testing.T) {
	m, r := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"sai": {Source: "owner/private.git"}, "mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "x@sai"}, {ID: "a@mp"}},
	})
	m.Agents.Providers = nil
	r.Missing("claude", "codex")
	marketplace(t, filepath.Join(m.Paths.Root, "mp"), "mp", map[string][]string{"a": {"s"}})
	o := m.opencodeBackend()
	changes, err := o.plan(context.Background(), m.desired(config.ProviderOpencode))
	if err != nil {
		t.Fatal(err)
	}
	apply(t, changes)
	if got := r.Lines(); len(got) != 1 || !strings.Contains(got[0], "https://github.com/owner/private.git ") {
		t.Errorf("ran %v, want one clone of the .git-less URL", got)
	}
	assertLink(t, filepath.Join(skillsDir(m), "s"), filepath.Join(m.Paths.Root, "mp", "plugins", "a", "skills", "s"))
}

func TestOpencodePluginsSharingARepo(t *testing.T) {
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"mp": {Source: "./mp"}},
		Plugins:      []config.Plugin{{ID: "docs@mp"}, {ID: "examples@mp"}},
	})
	root := filepath.Join(m.Paths.Root, "mp")
	writeFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), `{"name":"mp","plugins":[
		{"name":"docs","source":"./","strict":false,"skills":["./skills/pdf","./skills/xlsx"]},
		{"name":"examples","source":"./","strict":false,"skills":["./skills/art"]}]}`)
	for _, s := range []string{"pdf", "xlsx", "art", "unlisted"} {
		writeFile(t, filepath.Join(root, "skills", s, "SKILL.md"), "")
	}
	want := []string{"+ opencode skill art (examples@mp)", "+ opencode skill pdf (docs@mp)", "+ opencode skill xlsx (docs@mp)"}
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}
