package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func newInstructions(t *testing.T, providers ...string) (*Module, *runnertest.Fake, string) {
	t.Helper()
	m, fake := newModule(t, config.Agents{Providers: providers, Instructions: config.InstructionFiles{"AGENTS.md"}})
	src := filepath.Join(m.Paths.Root, "AGENTS.md")
	writeFile(t, src, "be terse\n")
	return m, fake, src
}

func groups(changes []engine.Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, c.Group+": "+string(c.Action)+" "+c.Target)
	}
	return out
}

func assertCopy(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if !info.Mode().IsRegular() {
		t.Errorf("%s is %v, want a regular file", path, info.Mode())
		return
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("%s mode = %o, want 444", path, info.Mode().Perm())
	}
	if got := readFile(t, path); got != want {
		t.Errorf("%s = %q, want %q", path, got, want)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s still there: %v", path, err)
	}
}

func TestInstructionsCopyEveryProvider(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode)
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude-dir")
	m.CodexHome = filepath.Join(m.Paths.Home, "codex-home")

	changes := plan(t, m)
	want := []string{
		"claude: + ~/claude-dir/CLAUDE.md",
		"codex: + ~/codex-home/AGENTS.md",
		"opencode: + ~/.config/opencode/AGENTS.md",
	}
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, "claude-dir")); err == nil {
		t.Error("plan created the claude dir")
	}
	assertGone(t, m.instructionFilesStatePath())
	apply(t, changes)
	assertCopy(t, filepath.Join(m.Paths.Home, "claude-dir", "CLAUDE.md"), "be terse\n")
	assertCopy(t, filepath.Join(m.Paths.Home, "codex-home", "AGENTS.md"), "be terse\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".config", "opencode", "AGENTS.md"), "be terse\n")
	assertConverged(t, m)
}

func TestInstructionsDefaultPaths(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "be terse\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), "be terse\n")
	assertConverged(t, m)
}

func TestInstructionsBackUpExistingFile(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	dst := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	writeFile(t, dst, "mine\n")
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.Contains(got[0], "back up to CLAUDE.md.zakwas-bak") {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertCopy(t, dst, "be terse\n")
	if got := readFile(t, dst+".zakwas-bak"); got != "mine\n" {
		t.Errorf("backup = %q", got)
	}
	assertConverged(t, m)
}

func TestInstructionsFollowSourceChange(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	writeFile(t, src, "be terser\n")
	changes := plan(t, m)
	if len(changes) != 1 || changes[0].Action != engine.Update || strings.Contains(changes[0].Detail, "back up") {
		t.Fatalf("plan = %v", targets(changes))
	}
	apply(t, changes)
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "be terser\n")
	assertConverged(t, m)
}

func TestInstructionsEditedCopyBackedUp(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	dst := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, "edited\n")
	writeFile(t, src, "be terser\n")
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.Contains(got[0], "edited in place, back up to CLAUDE.md.zakwas-bak") {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	if got := readFile(t, dst+".zakwas-bak"); got != "edited\n" {
		t.Errorf("backup = %q", got)
	}
	assertCopy(t, dst, "be terser\n")
}

func TestInstructionsRemovedWithProvider(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))

	m.Agents.Providers = []string{config.ProviderClaude}
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"instructions: - ~/.codex/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	if !changes[0].Destructive {
		t.Error("removal not destructive")
	}
	apply(t, changes)
	assertGone(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"))
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "be terse\n")
	assertConverged(t, m)
}

func TestInstructionsUnsetRemovesOwnCopiesOnly(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))
	codexCopy := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	if err := os.Remove(codexCopy); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(m.Paths.Root, "other.md")
	writeFile(t, other, "x")
	if err := os.Symlink(other, codexCopy); err != nil {
		t.Fatal(err)
	}

	m.Agents.Instructions = nil
	changes := plan(t, m)
	want := []string{
		"- ~/.claude/CLAUDE.md (instructions no longer managed)",
		"- ~/.codex/AGENTS.md (no longer zakwas's file, forget it)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if changes[1].Destructive {
		t.Error("forgetting a replaced file is destructive")
	}
	apply(t, changes)
	assertGone(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"))
	assertLink(t, codexCopy, other)
	assertGone(t, m.instructionFilesStatePath())
	assertConverged(t, m)
}

func TestInstructionsUnsetBacksUpEditedCopy(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	dst := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	if err := os.Chmod(dst, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, "edited\n")
	m.Agents.Instructions = nil
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.Contains(got[0], "edited: back up to CLAUDE.md.zakwas-bak") || changes[0].Destructive {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertGone(t, dst)
	if got := readFile(t, dst+".zakwas-bak"); got != "edited\n" {
		t.Errorf("backup = %q", got)
	}
	assertConverged(t, m)
}

func TestInstructionsAbsentChangesNothing(t *testing.T) {
	m, _ := newModule(t, config.Agents{})
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("plan = %v", targets(got))
	}
	assertGone(t, filepath.Join(m.Paths.Home, ".local"))
}

func TestInstructionsFollowConfigDirChange(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude-dir")
	want := []string{"claude: + ~/claude-dir/CLAUDE.md", "instructions: - ~/.claude/CLAUDE.md"}
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	apply(t, changes)
	assertCopy(t, filepath.Join(m.ClaudeConfigDir, "CLAUDE.md"), "be terse\n")
	assertGone(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"))
	assertConverged(t, m)
}

func TestInstructionsOptInAndBestEffort(t *testing.T) {
	m, fake, _ := newInstructions(t)
	m.Agents.Providers = nil
	fake.Missing("opencode")
	fake.OnOK(marketList, "[]").OnOK(pluginList, `{"installed":[],"available":[]}`)
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"claude: + ~/.claude/CLAUDE.md"}) {
		t.Fatalf("default providers, opencode missing: plan = %v", got)
	}
	apply(t, changes)

	fake.Install("opencode")
	if got := groups(plan(t, m)); !reflect.DeepEqual(got, []string{"opencode: + ~/.config/opencode/AGENTS.md"}) {
		t.Fatalf("opencode installed: plan = %v", got)
	}
	if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".config", "opencode", "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("best-effort conflict: plan = %v", targets(got))
	}
	m.Agents.Providers = []string{config.ProviderClaude, config.ProviderOpencode}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "opencode: instructions:") {
		t.Errorf("named opencode conflict: err = %v", err)
	}
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "be terse\n")
}

func TestInstructionsKeptWhileOpencodeMissing(t *testing.T) {
	m, fake, _ := newInstructions(t)
	m.Agents.Providers = nil
	fake.OnOK(marketList, "[]").OnOK(pluginList, `{"installed":[],"available":[]}`)
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".config", "opencode", "AGENTS.md"), "be terse\n")
	fake.Missing("opencode")
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("plan = %v", targets(got))
	}
}

func TestInstructionsPlanErrors(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	m.Agents.Instructions = config.InstructionFiles{"missing.md"}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "agents.instructions:") || !strings.Contains(err.Error(), "missing.md") {
		t.Errorf("missing source: err = %v", err)
	}
	m.Agents.Instructions = config.InstructionFiles{"AGENTS.md", "."}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("directory source: err = %v", err)
	}

	m.Agents.Instructions = config.InstructionFiles{"AGENTS.md"}
	if err := os.Symlink(m.Paths.Root, filepath.Join(m.Paths.Home, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("symlinked parent: err = %v", err)
	}
}

func TestInstructionsRefuseSymlinkedState(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(m.Paths.Home, ".local")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("err = %v", err)
	}
}

func TestInstructionsSharedPathWrittenOnce(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderCodex, config.ProviderOpencode)
	m.CodexHome = filepath.Join(m.Paths.Home, ".config", "opencode")
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: + ~/.config/opencode/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertCopy(t, filepath.Join(m.CodexHome, "AGENTS.md"), "be terse\n")
	assertConverged(t, m)
}

func TestInstructionsConfigDirAlias(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	real := filepath.Join(m.Paths.Home, "cc")
	m.ClaudeConfigDir = real
	apply(t, plan(t, m))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	m.ClaudeConfigDir = alias
	assertConverged(t, m)
	assertCopy(t, filepath.Join(real, "CLAUDE.md"), "be terse\n")
}

func TestInstructionsFailureSkipsProvider(t *testing.T) {
	m, fake, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	m.Agents.Marketplaces = map[string]config.Marketplace{"sai": {Source: "o/sai", Providers: []string{config.ProviderClaude}}}
	if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: instructions:") {
		t.Fatalf("err = %v", err)
	}
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: + ~/.codex/AGENTS.md"}) {
		t.Errorf("plan = %v", got)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("planned the failed provider's plugins: %v", fake.Lines())
	}
}

func TestInstructionsJoinFilesPerProvider(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	m.Vars = map[string]string{"team": "core"}
	m.Agents.Instructions = config.InstructionFiles{"base.md", "claude.md", "codex.md"}
	writeFile(t, filepath.Join(m.Paths.Root, "base.md"), "# Base\nhome {{ .Home }} team {{ .Vars.team }}\n\n")
	writeFile(t, filepath.Join(m.Paths.Root, "claude.md"), "{{ if eq .Provider \"claude\" }}use the Skill tool{{ end }}\n")
	writeFile(t, filepath.Join(m.Paths.Root, "codex.md"), "{{ if eq .Provider \"codex\" }}no skills here\n{{ end }}")

	apply(t, plan(t, m))
	base := "# Base\nhome " + m.Paths.Home + " team core"
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), base+"\n\nuse the Skill tool\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), base+"\n\nno skills here\n")
	assertConverged(t, m)
}

func TestInstructionsTemplateErrorFailsPlan(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	for name, body := range map[string]string{
		"unknown variable": "{{ .Nope }}\n",
		"bad syntax":       "{{ if }}\n",
	} {
		writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), body)
		if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "claude: instructions: render") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInstructionsImports(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	root := m.Paths.Root
	writeFile(t, filepath.Join(root, "AGENTS.md"), strings.Join([]string{
		"intro",
		"@docs/git.md",
		"see @docs/style.md, then `@docs/skipped.md` and @claude",
		"```",
		"@docs/skipped.md",
		"```",
		"",
	}, "\n"))
	writeFile(t, filepath.Join(root, "docs", "git.md"), "git rules\n@style.md\n")
	writeFile(t, filepath.Join(root, "docs", "style.md"), "{{ .Provider }} style\n")

	apply(t, plan(t, m))
	claude := "intro\n@" + filepath.Join(root, "docs", "git.md") + "\nsee @" + filepath.Join(root, "docs", "style.md") +
		", then `@docs/skipped.md` and @claude\n```\n@docs/skipped.md\n```\n"
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), claude)
	codex := "intro\ngit rules\n{{ .Provider }} style\nsee {{ .Provider }} style, then `@docs/skipped.md` and @claude\n```\n@docs/skipped.md\n```\n"
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), codex)
	assertConverged(t, m)
}

func TestInstructionsHomeImportKept(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	writeFile(t, filepath.Join(m.Paths.Home, "notes", "me.md"), "about me\n")
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "@~/notes/me.md\n")
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "@~/notes/me.md\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), "about me\n")
}

func TestInstructionsImportErrors(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderCodex)
	root := m.Paths.Root
	writeFile(t, filepath.Join(root, "a.md"), "@b.md\n")
	writeFile(t, filepath.Join(root, "b.md"), "@a.md\n")
	writeFile(t, filepath.Join(root, "d1.md"), "@d2.md\n")
	writeFile(t, filepath.Join(root, "d2.md"), "@d3.md\n")
	writeFile(t, filepath.Join(root, "d3.md"), "@d4.md\n")
	writeFile(t, filepath.Join(root, "d4.md"), "@d5.md\n")
	writeFile(t, filepath.Join(root, "d5.md"), "deep\n")
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"missing", "@nope/missing.md\n", "import @nope/missing.md"},
		{"missing nested", "@a.md and @gone.md\n", "gone.md"},
		{"cycle", "@a.md\n", "imports itself"},
		{"too deep", "@d1.md\n", "nest more than 4 files deep"},
		{"directory", "@docs/\n", "docs"},
		{"four hops", "@d2.md\n", ""},
		{"plain mention", "ping @someone\n", ""},
	} {
		for _, provider := range []string{config.ProviderClaude, config.ProviderCodex} {
			m.Agents.Providers = []string{provider}
			writeFile(t, filepath.Join(root, "AGENTS.md"), tc.body)
			_, err := m.Plan(context.Background())
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("%s/%s: err = %v", tc.name, provider, err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("%s/%s: err = %v, want %q", tc.name, provider, err, tc.want)
			}
		}
	}
}

func TestInstructionsProseMentionsAreNotImports(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "types from @types/node and @scope/pkg, not @docs/x.md\n")
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "import @docs/x.md") {
		t.Fatalf("a .md path that is missing must fail: err = %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("plan = %v", targets(changes))
	}

	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "types from @types/node and @scope/pkg.\n")
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "types from @types/node and @scope/pkg.\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), "types from @types/node and @scope/pkg.\n")
	assertConverged(t, m)

	writeFile(t, filepath.Join(m.Paths.Root, "types", "node"), "node types\n")
	apply(t, plan(t, m))
	assertCopy(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), "types from node types and @scope/pkg.\n")
}

func TestInstructionsImportPathWithSpaceFailsForClaude(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	m.Paths.Root = filepath.Join(t.TempDir(), "my repo")
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "@docs/git.md\n")
	writeFile(t, filepath.Join(m.Paths.Root, "docs", "git.md"), "git rules\n")
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: instructions:") || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("err = %v", err)
	}
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: + ~/.codex/AGENTS.md"}) {
		t.Errorf("plan = %v", got)
	}
}

func TestInstructionsBestEffortProviderRenderErrorSkipped(t *testing.T) {
	m, fake, _ := newInstructions(t)
	m.Agents.Providers = nil
	fake.OnOK(marketList, "[]").OnOK(pluginList, `{"installed":[],"available":[]}`)
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "{{ if eq .Provider \"opencode\" }}{{ .Nope }}{{ end }}hi\n")
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"claude: + ~/.claude/CLAUDE.md"}) {
		t.Fatalf("plan = %v", got)
	}
}

func TestInstructionsSizeLimit(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), strings.Repeat("x", 32768))
	if got := groups(plan(t, m)); len(got) != 2 {
		t.Fatalf("at the limit: plan = %v", got)
	}

	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), strings.Repeat("x", 32769))
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex: instructions: rendered instructions are 32769 bytes, over codex's 32768 byte limit") {
		t.Fatalf("err = %v", err)
	}
	if got := groups(changes); !reflect.DeepEqual(got, []string{"claude: + ~/.claude/CLAUDE.md"}) {
		t.Errorf("plan = %v", got)
	}
}

func TestInstructionsSizeLimitCountsInlinedImports(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "@big.md\n")
	writeFile(t, filepath.Join(m.Paths.Root, "big.md"), strings.Repeat("x", 40000))
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex") || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v", err)
	}
	if got := groups(changes); !reflect.DeepEqual(got, []string{"claude: + ~/.claude/CLAUDE.md"}) {
		t.Errorf("plan = %v", got)
	}
}

func TestInstructionsShared(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	m.Agents.SharedInstructions = true
	writeFile(t, filepath.Join(m.Paths.Root, "AGENTS.md"), "{{ .Provider }}\n")
	changes := plan(t, m)
	want := []string{"claude: + ~/.claude/CLAUDE.md", "shared: + ~/.agents/AGENTS.md"}
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	apply(t, changes)
	assertCopy(t, filepath.Join(m.Paths.Home, ".agents", "AGENTS.md"), "shared\n")
	assertCopy(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), "claude\n")
	assertConverged(t, m)

	m.Agents.SharedInstructions = false
	changes = plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"instructions: - ~/.agents/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertGone(t, filepath.Join(m.Paths.Home, ".agents", "AGENTS.md"))
}

func TestInstructionsSharedFailureIsReported(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	m.Agents.SharedInstructions = true
	if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".agents", "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "shared: instructions:") {
		t.Fatalf("err = %v", err)
	}
	if got := groups(changes); !reflect.DeepEqual(got, []string{"claude: + ~/.claude/CLAUDE.md"}) {
		t.Errorf("plan = %v", got)
	}
}

func legacyLink(t *testing.T, m *Module, provider, dst, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dst); err != nil {
		t.Fatal(err)
	}
	st := instructionsState{Links: map[string]instructionsLink{}}
	if data, err := os.ReadFile(m.instructionsStatePath()); err == nil {
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatal(err)
		}
	}
	st.Links[dst] = instructionsLink{Provider: provider, Target: src}
	if err := saveState(m.instructionsStatePath(), st); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionsReplaceEarlierLinks(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	claude := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	codex := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	legacyLink(t, m, config.ProviderClaude, claude, src)
	legacyLink(t, m, config.ProviderCodex, codex, src)

	changes := plan(t, m)
	want := []string{"claude: ~ ~/.claude/CLAUDE.md", "codex: ~ ~/.codex/AGENTS.md"}
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	for _, c := range changes {
		if strings.Contains(c.Detail, "back up") {
			t.Errorf("%s backs up zakwas's own link: %s", c.Target, c.Detail)
		}
	}
	apply(t, changes)
	assertCopy(t, claude, "be terse\n")
	assertCopy(t, codex, "be terse\n")
	for _, path := range []string{claude, codex} {
		assertGone(t, path+".zakwas-bak")
	}
	assertGone(t, m.instructionsStatePath())
	assertConverged(t, m)
	if got := readFile(t, src); got != "be terse\n" {
		t.Errorf("source = %q", got)
	}
}

func TestInstructionsEarlierLinkOfDroppedProviderRemoved(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	codex := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	legacyLink(t, m, config.ProviderCodex, codex, src)
	changes := plan(t, m)
	want := []string{"claude: + ~/.claude/CLAUDE.md", "codex: - ~/.codex/AGENTS.md"}
	if got := groups(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	apply(t, changes)
	assertGone(t, codex)
	assertGone(t, m.instructionsStatePath())
	assertConverged(t, m)
}

func TestInstructionsUsersLinkLeftAlone(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	codex := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, codex); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))
	assertLink(t, codex, src)
	assertConverged(t, m)
}

func TestInstructionsEarlierLinkNoLongerALink(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	claude := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	apply(t, plan(t, m))
	legacy := instructionsState{Links: map[string]instructionsLink{claude: {Provider: config.ProviderClaude, Target: src}}}
	if err := saveState(m.instructionsStatePath(), legacy); err != nil {
		t.Fatal(err)
	}
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.Contains(got[0], "stop tracking it as one") {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertGone(t, m.instructionsStatePath())
	assertConverged(t, m)
}

func TestInstructionsImmutableCopy(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	m.Immutable = true
	apply(t, plan(t, m))
	assertConverged(t, m)
	m.Agents.Instructions = nil
	apply(t, plan(t, m))
	assertGone(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"))
}

func TestInstructionsStateFileHoldsHashes(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	var st struct{ Files map[string]string }
	if err := json.Unmarshal([]byte(readFile(t, m.instructionFilesStatePath())), &st); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Files[filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")]; !ok {
		t.Errorf("state = %v", st.Files)
	}
	assertGone(t, m.instructionsStatePath())
}
