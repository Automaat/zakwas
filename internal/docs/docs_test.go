package docs_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/cli"
	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const (
	repoRoot   = "../.."
	skipMarker = "<!-- recipe: skip -->"
)

type block struct {
	file string
	line int
	body []byte
	skip bool
}

func (b block) name() string { return fmt.Sprintf("%s:%d", b.file, b.line) }

func docFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	out := []string{"README.md"}
	for _, f := range files {
		out = append(out, filepath.ToSlash(filepath.Join("docs", filepath.Base(f))))
	}
	return out
}

func yamlBlocks(t *testing.T, file string) []block {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, file))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	var blocks []block
	var cur *block
	fence := ""
	for i, text := range lines {
		n := i + 1
		trimmed := strings.TrimSpace(text)
		switch {
		case fence != "" && trimmed == fence:
			if cur != nil {
				blocks = append(blocks, *cur)
			}
			cur, fence = nil, ""
		case fence != "":
			if cur != nil {
				cur.body = append(cur.body, text...)
				cur.body = append(cur.body, '\n')
			}
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
			lang := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "`~")))
			if lang != "yaml" && lang != "yml" && !strings.HasPrefix(lang, "yaml ") && !strings.HasPrefix(lang, "yml ") {
				continue
			}
			if text != "```yaml" {
				t.Errorf("%s:%d: write a yaml fence as ```yaml at the start of the line so it is checked", file, n)
				continue
			}
			cur = &block{file: file, line: n}
			cur.skip = i > 0 && strings.TrimSpace(lines[i-1]) == skipMarker
		case trimmed == skipMarker:
			if i+1 >= len(lines) || lines[i+1] != "```yaml" {
				t.Errorf("%s:%d: %s must sit on the line before a yaml fence", file, n, skipMarker)
			}
		}
	}
	if fence != "" {
		t.Fatalf("%s: unterminated code fence", file)
	}
	return blocks
}

func parseBlock(b block, root, home string) (*config.Config, error) {
	return parseBlockFor(b, root, home, platform.Darwin)
}

func parseBlockFor(b block, root, home, goos string) (*config.Config, error) {
	return config.ParseFor(b.body, filepath.Join(root, config.FileName), home, goos)
}

func TestDocumentedYAMLBlocksLoad(t *testing.T) {
	checked := 0
	for _, file := range docFiles(t) {
		for _, b := range yamlBlocks(t, file) {
			if b.skip {
				continue
			}
			checked++
			t.Run(b.name(), func(t *testing.T) {
				if _, err := parseBlock(b, t.TempDir(), t.TempDir()); err != nil {
					t.Errorf("%s does not load as a zakwas.yaml (mark it with %s if it is not one): %v", b.name(), skipMarker, err)
				}
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no yaml blocks")
	}
}

func TestDocumentedAgentsBlocksLoadOnLinux(t *testing.T) {
	loaded := 0
	for _, file := range docFiles(t) {
		for _, b := range yamlBlocks(t, file) {
			if b.skip || !isAgentsBlock(t, b) {
				continue
			}
			loaded++
			t.Run(b.name(), func(t *testing.T) {
				if _, err := parseBlockFor(b, t.TempDir(), t.TempDir(), platform.Linux); err != nil {
					t.Errorf("%s does not load on Linux: %v", b.name(), err)
				}
			})
		}
	}
	if loaded == 0 {
		t.Fatal("found no agents blocks")
	}
}

func configKeys() []string {
	typ := reflect.TypeFor[config.Config]()
	var keys []string
	for f := range typ.Fields() {
		if name, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

func TestSkippedYAMLBlocksAreNotConfigs(t *testing.T) {
	known := configKeys()
	if !slices.Contains(known, "agents") {
		t.Fatalf("config keys %v lack agents", known)
	}
	for _, file := range docFiles(t) {
		for _, b := range yamlBlocks(t, file) {
			if !b.skip {
				continue
			}
			t.Run(b.name(), func(t *testing.T) {
				var doc map[string]any
				if err := yaml.Unmarshal(b.body, &doc); err != nil {
					return
				}
				for key := range doc {
					if slices.Contains(known, key) {
						t.Errorf("%s has the zakwas.yaml key %q: remove %s", b.name(), key, skipMarker)
					}
				}
			})
		}
	}
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createReferencedFiles(t *testing.T, cfg *config.Config, paths config.Paths) {
	t.Helper()
	resolve := func(p string) string {
		if p == "~" || strings.HasPrefix(p, "~/") {
			return paths.Dst(p)
		}
		return paths.Src(p)
	}
	for _, f := range cfg.Agents.Instructions {
		writeFixture(t, resolve(f), "# Instructions\n")
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Agents.Profiles)) {
		if prompt := cfg.Agents.Profiles[name].Prompt; prompt != "" {
			writeFixture(t, resolve(prompt), "Review changes.\n")
		}
	}
	for _, s := range cfg.Agents.Skills {
		if s.Git != "" {
			continue
		}
		body := "---\nname: " + s.Name() + "\ndescription: A skill\n---\n"
		writeFixture(t, filepath.Join(resolve(s.Path), "SKILL.md"), body)
	}
	for _, f := range cfg.Templates.Files {
		writeFixture(t, resolve(f.Src), "{}\n")
	}
}

func declaresAgentState(a *config.Agents) bool {
	return len(a.Instructions) > 0 || len(a.MCP) > 0 || len(a.Skills) > 0 || len(a.Profiles) > 0 ||
		a.Permissions != nil || len(a.Plugins) > 0 || len(a.Marketplaces) > 0 || a.Codex != nil || a.Hooks != nil
}

func isAgentsBlock(t *testing.T, b block) bool {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(b.body, &doc); err != nil {
		return false
	}
	if _, ok := doc["agents"]; !ok {
		return false
	}
	for key := range doc {
		if key != "agents" && key != "templates" {
			return false
		}
	}
	return true
}

func TestDocumentedAgentsBlocksPlan(t *testing.T) {
	planned := 0
	for _, file := range docFiles(t) {
		for _, b := range yamlBlocks(t, file) {
			if b.skip || !isAgentsBlock(t, b) {
				continue
			}
			planned++
			t.Run(b.name(), func(t *testing.T) {
				root, home := t.TempDir(), t.TempDir()
				cfg, err := parseBlock(b, root, home)
				if err != nil {
					t.Fatalf("%s does not load: %v", b.name(), err)
				}
				createReferencedFiles(t, cfg, config.Paths{Home: home, Root: cfg.Root})
				fake := runnertest.New().
					OnOK("claude plugin marketplace list --json", "[]").
					OnOK("claude plugin list --json --available", `{"installed": [], "available": []}`).
					OnOK("codex plugin marketplace list --json", `{"marketplaces": []}`).
					OnOK("codex plugin list --available --json", `{"installed": [], "available": []}`).
					OnOK("copilot plugin marketplace list --json", "[]").
					OnOK("copilot plugin list --json", "[]")
				mods := cli.Modules(cfg, cli.Env{Home: home, Cwd: root, Runner: fake, GOOS: platform.Darwin})
				i := slices.IndexFunc(mods, func(m engine.Module) bool { return m.Name() == "agents" })
				if i < 0 {
					t.Fatalf("%s: no agents module", b.name())
				}
				changes, err := mods[i].Plan(context.Background())
				if errors.Is(err, install.ErrLinksDenied) {
					t.Skipf("%s links skills and this machine denies links: %v", b.name(), err)
				}
				if err != nil {
					t.Fatalf("%s does not plan: %v", b.name(), err)
				}
				if declaresAgentState(cfg.Agents) && len(changes) == 0 {
					t.Errorf("%s plans no change", b.name())
				}
			})
		}
	}
	if planned == 0 {
		t.Fatal("found no agents blocks")
	}
}
