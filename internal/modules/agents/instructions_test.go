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
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func newInstructions(t *testing.T, providers ...string) (*Module, *runnertest.Fake, string) {
	t.Helper()
	m, fake := newModule(t, config.Agents{Providers: providers, Instructions: "AGENTS.md"})
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

func TestInstructionsLinkEveryProvider(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode)
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
	if _, err := os.Lstat(m.instructionsStatePath()); err == nil {
		t.Error("plan wrote the state file")
	}
	apply(t, changes)
	assertLink(t, filepath.Join(m.Paths.Home, "claude-dir", "CLAUDE.md"), src)
	assertLink(t, filepath.Join(m.Paths.Home, "codex-home", "AGENTS.md"), src)
	assertLink(t, filepath.Join(m.Paths.Home, ".config", "opencode", "AGENTS.md"), src)
	assertConverged(t, m)
}

func TestInstructionsDefaultPaths(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), src)
	assertLink(t, filepath.Join(m.Paths.Home, ".codex", "AGENTS.md"), src)
	assertConverged(t, m)
}

func TestInstructionsBackUpExistingFile(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	dst := filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")
	writeFile(t, dst, "mine\n")
	changes := plan(t, m)
	if got := targets(changes); len(got) != 1 || !strings.Contains(got[0], "back up to CLAUDE.md.zakwas-bak") {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertLink(t, dst, src)
	if got := readFile(t, dst+".zakwas-bak"); got != "mine\n" {
		t.Errorf("backup = %q", got)
	}
	assertConverged(t, m)
}

func TestInstructionsRemovedWithProvider(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))

	m.Agents.Providers = []string{config.ProviderClaude}
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: - ~/.codex/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	if !changes[0].Destructive {
		t.Error("removal not destructive")
	}
	apply(t, changes)
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("codex link still there: %v", err)
	}
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), src)
	assertConverged(t, m)
}

func TestInstructionsUnsetRemovesOwnLinksOnly(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	apply(t, plan(t, m))
	codexLink := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	if err := os.Remove(codexLink); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(m.Paths.Root, "other.md")
	writeFile(t, other, "x")
	if err := os.Symlink(other, codexLink); err != nil {
		t.Fatal(err)
	}

	m.Agents.Instructions = ""
	changes := plan(t, m)
	want := []string{
		"- ~/.claude/CLAUDE.md (claude instructions)",
		"- ~/.codex/AGENTS.md (no longer zakwas's link, forget it)",
	}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if changes[1].Destructive {
		t.Error("forgetting a changed link is destructive")
	}
	apply(t, changes)
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("claude link still there: %v", err)
	}
	assertLink(t, codexLink, other)
	assertConverged(t, m)
}

func TestInstructionsAbsentChangesNothing(t *testing.T) {
	m, _ := newModule(t, config.Agents{})
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("plan = %v", targets(got))
	}
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".local")); !os.IsNotExist(err) {
		t.Errorf("plan touched ~/.local: %v", err)
	}
}

func TestInstructionsUsersLinkLeftAlone(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude, config.ProviderCodex)
	codexLink := filepath.Join(m.Paths.Home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(codexLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, codexLink); err != nil {
		t.Fatal(err)
	}
	apply(t, plan(t, m))
	m.Agents.Providers = []string{config.ProviderClaude}
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("plan removes the user's link: %v", targets(got))
	}
}

func TestInstructionsFollowConfigDirChange(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	m.ClaudeConfigDir = filepath.Join(m.Paths.Home, "claude-dir")
	want := []string{"+ ~/claude-dir/CLAUDE.md", "- ~/.claude/CLAUDE.md"}
	changes := plan(t, m)
	var got []string
	for _, c := range changes {
		got = append(got, string(c.Action)+" "+c.Target)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(m.ClaudeConfigDir, "CLAUDE.md"), src)
	assertConverged(t, m)
}

func TestInstructionsRelinkOnSourceChange(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	apply(t, plan(t, m))
	writeFile(t, filepath.Join(m.Paths.Root, "CLAUDE.md"), "y")
	m.Agents.Instructions = "CLAUDE.md"
	changes := plan(t, m)
	if len(changes) != 1 || changes[0].Action != engine.Update {
		t.Fatalf("plan = %v", targets(changes))
	}
	apply(t, changes)
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), filepath.Join(m.Paths.Root, "CLAUDE.md"))
	assertConverged(t, m)
	m.Agents.Instructions = ""
	apply(t, plan(t, m))
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("relinked link not removed: %v", err)
	}
}

func TestInstructionsOptInAndBestEffort(t *testing.T) {
	m, fake, src := newInstructions(t)
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
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md"), src)
}

func TestInstructionsKeptWhileOpencodeMissing(t *testing.T) {
	m, fake, src := newInstructions(t)
	m.Agents.Providers = nil
	fake.OnOK(marketList, "[]").OnOK(pluginList, `{"installed":[],"available":[]}`)
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, ".config", "opencode", "AGENTS.md"), src)
	fake.Missing("opencode")
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("plan = %v", targets(got))
	}
}

func TestInstructionsPlanErrors(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	m.Agents.Instructions = "missing.md"
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "agents.instructions:") || !strings.Contains(err.Error(), "missing.md") {
		t.Errorf("missing source: err = %v", err)
	}
	m.Agents.Instructions = "."
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("directory source: err = %v", err)
	}

	m.Agents.Instructions = "AGENTS.md"
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

func TestInstructionsSharedPathLinkedOnce(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderCodex, config.ProviderOpencode)
	m.CodexHome = filepath.Join(m.Paths.Home, ".config", "opencode")
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: + ~/.config/opencode/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(m.CodexHome, "AGENTS.md"), src)
	assertConverged(t, m)
}

func TestInstructionsConfigDirAlias(t *testing.T) {
	m, _, src := newInstructions(t, config.ProviderClaude)
	real := filepath.Join(m.Paths.Home, "cc")
	m.ClaudeConfigDir = real
	apply(t, plan(t, m))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	m.ClaudeConfigDir = alias
	assertConverged(t, m)
	assertLink(t, filepath.Join(real, "CLAUDE.md"), src)
}

func TestInstructionsNeverLinkUntracked(t *testing.T) {
	m, _, _ := newInstructions(t, config.ProviderClaude)
	stateDir := filepath.Dir(m.instructionsStatePath())
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })
	changes := plan(t, m)
	if err := enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}}); err == nil {
		t.Fatal("apply succeeded without a writable state dir")
	}
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("link made without recording it: %v", err)
	}
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

func TestInstructionsSharedPathChangesOwner(t *testing.T) {
	m, fake, src := newInstructions(t, config.ProviderCodex, config.ProviderOpencode)
	m.CodexHome = filepath.Join(m.Paths.Home, ".config", "opencode")
	apply(t, plan(t, m))
	m.Agents.Providers = []string{config.ProviderOpencode}
	fake.Install("opencode")
	if got := targets(plan(t, m)); !reflect.DeepEqual(got, []string{"~ ~/.config/opencode/AGENTS.md (track as opencode instructions)"}) {
		t.Fatalf("plan = %v", got)
	}
	apply(t, plan(t, m))
	assertConverged(t, m)
	m.Agents.Providers = []string{config.ProviderCodex}
	changes := plan(t, m)
	if got := groups(changes); !reflect.DeepEqual(got, []string{"codex: ~ ~/.config/opencode/AGENTS.md"}) {
		t.Fatalf("plan = %v", got)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(m.CodexHome, "AGENTS.md"), src)
}
