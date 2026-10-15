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

func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: " + name + " skill\n---\nBody\n"
}

func localSkill(t *testing.T, m *Module, rel, name string) string {
	t.Helper()
	dir := filepath.Join(m.Paths.Root, rel)
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD(name))
	return dir
}

func newSkills(t *testing.T, a config.Agents) (*Module, *cloneRunner) {
	t.Helper()
	if a.Providers == nil {
		a.Providers = []string{config.ProviderClaude}
	}
	r := &cloneRunner{Fake: runnertest.New(), repos: map[string]func(string){}}
	home := t.TempDir()
	return &Module{Agents: a, Paths: config.Paths{Home: home, Root: t.TempDir()}, Runner: r, ClaudeConfigDir: filepath.Join(home, ".claude")}, r
}

func runApply(changes []engine.Change) error {
	return enginetest.Apply(context.Background(), engine.Plan{{Module: "agents", Changes: changes}})
}

func skillRepo(t *testing.T, skills map[string]string) func(string) {
	t.Helper()
	return func(dir string) {
		for path, name := range skills {
			writeFile(t, filepath.Join(dir, filepath.FromSlash(path), "SKILL.md"), skillMD(name))
		}
	}
}

func TestSkillsLinkEveryProvider(t *testing.T) {
	m, r := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex, config.ProviderOpencode},
		Skills:    []config.Skill{{Path: "skills/mine"}, {Path: "~/extra/other"}},
	})
	mine := localSkill(t, m, "skills/mine", "mine")
	writeFile(t, filepath.Join(m.Paths.Home, "extra", "other", "SKILL.md"), skillMD("other"))
	other := filepath.Join(m.Paths.Home, "extra", "other")

	changes := plan(t, m)
	got := targets(changes)
	wantTargets := []string{
		"+ ~/.claude/skills/mine (→ " + mine + ")",
		"+ ~/.claude/skills/other (→ ~/extra/other)",
		"+ ~/.agents/skills/mine (→ " + mine + ")",
		"+ ~/.agents/skills/other (→ ~/extra/other)",
	}
	if !reflect.DeepEqual(got, wantTargets) {
		t.Fatalf("got %v\nwant %v", got, wantTargets)
	}
	if len(r.Calls) != 0 {
		t.Errorf("plan ran %v", r.Lines())
	}
	for i, group := range []string{"claude", "claude", "codex", "codex"} {
		if changes[i].Group != group {
			t.Errorf("change %d group = %q, want %q", i, changes[i].Group, group)
		}
	}
	apply(t, changes)
	for _, dir := range []string{filepath.Join(m.Paths.Home, ".claude", "skills"), filepath.Join(m.Paths.Home, ".agents", "skills")} {
		assertLink(t, filepath.Join(dir, "mine"), mine)
		assertLink(t, filepath.Join(dir, "other"), other)
	}
	assertConverged(t, m)
}

func TestSkillsClaudeOnlyLeavesSharedDir(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	dir := localSkill(t, m, "skills/mine", "mine")
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "skills", "mine"), dir)
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".agents")); !os.IsNotExist(err) {
		t.Errorf("claude-only config touched ~/.agents: %v", err)
	}
	assertConverged(t, m)
}

func TestSkillsProvidersOfASkill(t *testing.T) {
	m, _ := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Skills:    []config.Skill{{Path: "skills/a", Providers: []string{config.ProviderCodex}}, {Path: "skills/b", Providers: []string{config.ProviderClaude}}},
	})
	a := localSkill(t, m, "skills/a", "a")
	b := localSkill(t, m, "skills/b", "b")
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "a"), a)
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "skills", "b"), b)
	for _, gone := range []string{".claude/skills/a", ".agents/skills/b"} {
		if _, err := os.Lstat(filepath.Join(m.Paths.Home, gone)); !os.IsNotExist(err) {
			t.Errorf("%s exists: %v", gone, err)
		}
	}
	assertConverged(t, m)
}

func TestSkillsOpencodeByDefaultIsBestEffort(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	m.Agents.Providers = nil
	localSkill(t, m, "skills/mine", "mine")
	r.Missing("opencode", "codex")
	apply(t, plan(t, m))
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".agents")); !os.IsNotExist(err) {
		t.Errorf("opencode is not installed, but ~/.agents exists: %v", err)
	}
	r.Install("opencode")
	apply(t, plan(t, m))
	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "mine"), filepath.Join(m.Paths.Root, "skills", "mine"))
	assertConverged(t, m)
}

func TestSkillsFailTheirProvider(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, m *Module)
		want  string
	}{
		{
			name: "name differs from the directory",
			setup: func(t *testing.T, m *Module) {
				writeFile(t, filepath.Join(m.Paths.Root, "skills", "mine", "SKILL.md"), skillMD("other"))
			},
			want: `claude: skills: skill "mine": SKILL.md of`,
		},
		{
			name: "no name",
			setup: func(t *testing.T, m *Module) {
				writeFile(t, filepath.Join(m.Paths.Root, "skills", "mine", "SKILL.md"), "---\ndescription: x\n---\n")
			},
			want: "has no name in its frontmatter",
		},
		{
			name: "no frontmatter",
			setup: func(t *testing.T, m *Module) {
				writeFile(t, filepath.Join(m.Paths.Root, "skills", "mine", "SKILL.md"), "name: mine\n")
			},
			want: "has no name in its frontmatter",
		},
		{
			name: "no SKILL.md",
			setup: func(t *testing.T, m *Module) {
				if err := os.MkdirAll(filepath.Join(m.Paths.Root, "skills", "mine"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "has no SKILL.md",
		},
		{
			name:  "missing directory",
			setup: func(*testing.T, *Module) {},
			want:  "does not exist",
		},
		{
			name: "not a directory",
			setup: func(t *testing.T, m *Module) {
				writeFile(t, filepath.Join(m.Paths.Root, "skills", "mine"), "x")
			},
			want: "is not a directory",
		},
		{
			name: "directory zakwas did not create",
			setup: func(t *testing.T, m *Module) {
				localSkill(t, m, "skills/mine", "mine")
				writeFile(t, filepath.Join(m.Paths.Home, ".claude", "skills", "mine", "SKILL.md"), skillMD("mine"))
			},
			want: "already exists and zakwas did not link it",
		},
		{
			name: "link zakwas did not create",
			setup: func(t *testing.T, m *Module) {
				localSkill(t, m, "skills/mine", "mine")
				elsewhere := filepath.Join(m.Paths.Home, ".agents", "skills", "mine")
				writeFile(t, filepath.Join(elsewhere, "SKILL.md"), skillMD("mine"))
				if err := os.MkdirAll(filepath.Join(m.Paths.Home, ".claude", "skills"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, filepath.Join(m.Paths.Home, ".claude", "skills", "mine")); err != nil {
					t.Fatal(err)
				}
			},
			want: "already exists and zakwas did not link it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
			tt.setup(t, m)
			changes, err := m.Plan(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("plan error = %v, want %q", err, tt.want)
			}
			if len(changes) != 0 {
				t.Errorf("failing provider still planned %v", targets(changes))
			}
		})
	}
}

func TestSkillsFailureLeavesOtherProviders(t *testing.T) {
	m, _ := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Skills:    []config.Skill{{Path: "skills/mine"}},
	})
	dir := localSkill(t, m, "skills/mine", "mine")
	writeFile(t, filepath.Join(m.Paths.Home, ".claude", "skills", "mine", "SKILL.md"), skillMD("mine"))
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: skills") {
		t.Fatalf("plan error = %v", err)
	}
	if got, want := targets(changes), []string{"+ ~/.agents/skills/mine (→ " + dir + ")"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSkillsLeaveAnEquivalentUserLinkAlone(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	dir := localSkill(t, m, "skills/mine", "mine")
	link := filepath.Join(m.Paths.Home, ".claude", "skills", "mine")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, m)
	m.Agents.Skills = nil
	if got := plan(t, m); len(got) != 0 {
		t.Errorf("removing the skill plans %v for a link zakwas did not make", targets(got))
	}
	assertLink(t, link, dir)
}

func TestSkillsFollowAMovedSource(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "old/mine"}}})
	oldDir := localSkill(t, m, "old/mine", "mine")
	apply(t, plan(t, m))
	link := filepath.Join(m.Paths.Home, ".claude", "skills", "mine")
	assertLink(t, link, oldDir)

	m.Agents.Skills = []config.Skill{{Path: "new/mine"}}
	newDir := localSkill(t, m, "new/mine", "mine")
	changes := plan(t, m)
	if got, want := targets(changes), []string{"~ ~/.claude/skills/mine (→ " + newDir + " (was " + oldDir + "))"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	apply(t, changes)
	assertLink(t, link, newDir)
	assertConverged(t, m)
}

func TestSkillsRemoveOnlyTheirOwnLinks(t *testing.T) {
	m, _ := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Skills:    []config.Skill{{Path: "skills/a"}, {Path: "skills/b"}, {Path: "skills/c"}},
	})
	for _, n := range []string{"a", "b", "c"} {
		localSkill(t, m, "skills/"+n, n)
	}
	apply(t, plan(t, m))
	statePath := SkillsStatePath(m.Paths.Home)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(m.Paths.Home, ".claude", "skills")
	agentsDir := filepath.Join(m.Paths.Home, ".agents", "skills")
	foreign := filepath.Join(m.Paths.Home, "foreign")
	writeFile(t, filepath.Join(foreign, "SKILL.md"), skillMD("b"))
	if err := os.Remove(filepath.Join(claude, "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(claude, "b")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m.Paths.Home, ".agents", ".skill-lock.json"), "{}")

	m.Agents.Skills = []config.Skill{{Path: "skills/a"}}
	changes := plan(t, m)
	var removed []string
	for _, c := range changes {
		if c.Action != "-" {
			t.Errorf("unexpected change %v", c)
		}
		removed = append(removed, c.Target+" "+c.Summary())
	}
	if len(removed) != 4 {
		t.Fatalf("got %v, want the four links of b and c", removed)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(claude, "a"), filepath.Join(m.Paths.Root, "skills", "a"))
	assertLink(t, filepath.Join(agentsDir, "a"), filepath.Join(m.Paths.Root, "skills", "a"))
	assertLink(t, filepath.Join(claude, "b"), foreign)
	for _, gone := range []string{filepath.Join(claude, "c"), filepath.Join(agentsDir, "b"), filepath.Join(agentsDir, "c")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.Paths.Home, ".agents", ".skill-lock.json")); err != nil {
		t.Errorf("npx skills lock file touched: %v", err)
	}
	assertConverged(t, m)

	m.Agents = config.Agents{}
	apply(t, plan(t, m))
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state file stays after the last skill: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(claude, "a")); !os.IsNotExist(err) {
		t.Errorf("link of a stays: %v", err)
	}
}

func TestSkillsGitSource(t *testing.T) {
	m, r := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Skills: []config.Skill{
			{Git: "Owner/skills", Path: "skills/pdf", Ref: "v1"},
			{Git: "https://github.com/owner/skills.git", Path: "extra/docx", Ref: "v1"},
		},
	})
	r.repos["https://github.com/owner/skills.git"] = skillRepo(t, map[string]string{"skills/pdf": "pdf", "extra/docx": "docx"})

	changes := plan(t, m)
	got := targets(changes)
	want := []string{
		"+ claude skills (clone https://github.com/owner/skills.git#v1, link docx, pdf)",
		"+ codex skills (clone https://github.com/owner/skills.git#v1, link docx, pdf)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if len(r.Calls) != 0 {
		t.Errorf("plan ran %v", r.Lines())
	}
	apply(t, changes)
	clone := r.Calls[0]
	if got := strings.Join(clone.Args[:6], " "); got != "clone --quiet --depth 1 --branch v1" || clone.Dir != "/" {
		t.Errorf("clone = %v in %q", clone.Args, clone.Dir)
	}
	if len(r.Calls) != 1 {
		t.Errorf("ran %v, want one clone", r.Lines())
	}
	cache := filepath.Join(m.skillsCacheDir(), newSkillSource(m.Agents.Skills[0]).id)
	for _, dir := range []string{filepath.Join(m.Paths.Home, ".claude", "skills"), filepath.Join(m.Paths.Home, ".agents", "skills")} {
		assertLink(t, filepath.Join(dir, "pdf"), filepath.Join(cache, "skills", "pdf"))
		assertLink(t, filepath.Join(dir, "docx"), filepath.Join(cache, "extra", "docx"))
	}
	assertConverged(t, m)
	if len(r.Calls) != 1 {
		t.Errorf("a plan after the clone ran %v", r.Lines())
	}

	r.Fake = runnertest.New()
	repo := "git --git-dir=" + filepath.Join(cache, ".git") + " --work-tree=" + cache
	r.OnOK(repo+" fetch --quiet --depth 1 origin v1", "")
	r.OnOK(repo+" reset --quiet --hard FETCH_HEAD", "")
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Lines()); got != 2 {
		t.Errorf("refresh ran %v", r.Lines())
	}
}

func TestSkillsRefreshSkipsUnclonedSources(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	if err := m.Refresh(context.Background()); err != nil || len(r.Lines()) != 0 {
		t.Errorf("refresh before the first apply: %v, ran %v", err, r.Lines())
	}
}

func TestSkillsRefreshReportsAFailedPull(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	apply(t, plan(t, m))
	r.Fake = runnertest.New()
	if err := m.Refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "skills source o/skills") {
		t.Errorf("refresh error = %v", err)
	}
}

func TestSkillsGitNameMismatchLinksNothing(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "good"}, {Git: "o/skills", Path: "bad"}}})
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"good": "good", "bad": "worse"})
	err := runApply(plan(t, m))
	if err == nil || !strings.Contains(err.Error(), `skill "bad": SKILL.md of o/skills bad names it "worse"`) {
		t.Fatalf("apply error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".claude", "skills", "good")); !os.IsNotExist(err) {
		t.Errorf("the good skill was linked next to a bad one: %v", err)
	}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "names it") {
		t.Errorf("the next plan error = %v, want the mismatch", err)
	}
}

func TestSkillsGitSkillOutsideItsRepoFails(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "evil"}}})
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "SKILL.md"), skillMD("evil"))
	r.repos["https://github.com/o/skills.git"] = func(dir string) {
		if err := os.Symlink(outside, filepath.Join(dir, "evil")); err != nil {
			t.Error(err)
		}
	}
	if err := runApply(plan(t, m)); err == nil || !strings.Contains(err.Error(), "resolves outside its repository") {
		t.Errorf("apply error = %v", err)
	}
}

func TestSkillsGitNeedsGit(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	r.Missing("git")
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "needs git to fetch o/skills") {
		t.Errorf("plan error = %v", err)
	}
}

func TestSkillsFailedCloneKeepsNothing(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/gone", Path: "pdf"}}})
	if err := runApply(plan(t, m)); err == nil {
		t.Fatal("apply of an unknown repository succeeded")
	}
	if entries, _ := os.ReadDir(m.skillsCacheDir()); len(entries) != 0 {
		t.Errorf("failed clone left %v", entries)
	}
	if _, err := os.Stat(SkillsStatePath(m.Paths.Home)); !os.IsNotExist(err) {
		t.Errorf("failed clone left a state file: %v", err)
	}
}

func TestSkillsDropClonesNoSkillUses(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	apply(t, plan(t, m))
	cache := filepath.Join(m.skillsCacheDir(), newSkillSource(m.Agents.Skills[0]).id)
	if _, err := os.Stat(cache); err != nil {
		t.Fatal(err)
	}

	m.Agents.Skills = []config.Skill{{Git: "o/skills", Path: "pdf", Ref: "v2"}}
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	changes := plan(t, m)
	apply(t, changes)
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "skills", "pdf"), filepath.Join(m.skillsCacheDir(), newSkillSource(m.Agents.Skills[0]).id, "pdf"))
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("the clone of the old ref stays: %v", err)
	}
	assertConverged(t, m)

	m.Agents.Skills = nil
	apply(t, plan(t, m))
	if entries, _ := os.ReadDir(m.skillsCacheDir()); len(entries) != 0 {
		t.Errorf("cache keeps %v", entries)
	}
	if _, err := os.Lstat(filepath.Join(m.Paths.Home, ".claude", "skills", "pdf")); !os.IsNotExist(err) {
		t.Errorf("link stays: %v", err)
	}
}

func TestSkillsRefuseASymlinkedStateFile(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	localSkill(t, m, "skills/mine", "mine")
	state := SkillsStatePath(m.Paths.Home)
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(m.Paths.Root, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Errorf("plan error = %v", err)
	}
}

func TestSkillMDName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"---\nname: pdf\n---\n", "pdf", true},
		{"---\r\nname: \"pdf\"\r\ndescription: x: y\r\n---\r\n", "pdf", true},
		{"\ufeff---\nname: 'pdf'\n---\n", "pdf", true},
		{"---\ndescription: a\nname:   pdf  \n---\nname: other\n", "pdf", true},
		{"---\ndescription: a\n---\nname: pdf\n", "", false},
		{"---\nname:\n---\n", "", false},
		{"name: pdf\n", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got, ok := skillMDName([]byte(tt.in)); got != tt.want || ok != tt.ok {
			t.Errorf("skillMDName(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestSkillsMovedToAFailingSourceKeepTheOldClone(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/a", Path: "pdf"}}})
	r.repos["https://github.com/o/a.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	apply(t, plan(t, m))
	link := filepath.Join(m.Paths.Home, ".claude", "skills", "pdf")

	m.Agents.Skills = []config.Skill{{Git: "o/gone", Path: "pdf"}}
	if err := runApply(plan(t, m)); err == nil {
		t.Fatal("apply of an unknown repository succeeded")
	}
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Errorf("the skill that worked is dangling: %v", err)
	}

	r.repos["https://github.com/o/gone.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	apply(t, plan(t, m))
	if _, err := os.Stat(filepath.Join(link, "SKILL.md")); err != nil {
		t.Errorf("the moved skill is dangling: %v", err)
	}
	if entries, _ := os.ReadDir(m.skillsCacheDir()); len(entries) != 1 {
		t.Errorf("cache holds %v, want only the new clone", entries)
	}
	assertConverged(t, m)
}

func TestSkillsSharedCloneSurvivesAFailingProvider(t *testing.T) {
	m, r := newSkills(t, config.Agents{
		Providers: []string{config.ProviderClaude, config.ProviderCodex},
		Skills:    []config.Skill{{Git: "o/skills", Path: "pdf"}},
	})
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	writeFile(t, filepath.Join(m.Paths.Home, ".claude", "skills", "pdf", "SKILL.md"), skillMD("pdf"))
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claude: skills") {
		t.Fatalf("plan error = %v", err)
	}
	apply(t, changes)
	cache := filepath.Join(m.skillsCacheDir(), newSkillSource(m.Agents.Skills[0]).id)
	assertLink(t, filepath.Join(m.Paths.Home, ".agents", "skills", "pdf"), filepath.Join(cache, "pdf"))
}

func TestSkillsForeignLinkFailsBeforeTheClone(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	writeFile(t, filepath.Join(m.Paths.Home, ".claude", "skills", "pdf", "SKILL.md"), skillMD("pdf"))
	changes, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already exists and zakwas did not link it") {
		t.Fatalf("plan error = %v", err)
	}
	if len(changes) != 0 || len(r.Calls) != 0 {
		t.Errorf("planned %v, ran %v", targets(changes), r.Lines())
	}
}

func TestSkillsBestEffortSkipsAForeignDirWhenApplying(t *testing.T) {
	m, r := newSkills(t, config.Agents{Skills: []config.Skill{{Git: "o/skills", Path: "pdf"}}})
	m.Agents.Providers = nil
	r.Missing("codex")
	r.repos["https://github.com/o/skills.git"] = skillRepo(t, map[string]string{"pdf": "pdf"})
	writeFile(t, filepath.Join(m.Paths.Home, ".agents", "skills", "pdf", "SKILL.md"), skillMD("pdf"))
	apply(t, plan(t, m))
	cache := filepath.Join(m.skillsCacheDir(), newSkillSource(m.Agents.Skills[0]).id)
	assertLink(t, filepath.Join(m.Paths.Home, ".claude", "skills", "pdf"), filepath.Join(cache, "pdf"))
	if info, err := os.Lstat(filepath.Join(m.Paths.Home, ".agents", "skills", "pdf")); err != nil || !info.IsDir() {
		t.Errorf("the foreign skill directory changed: %v", err)
	}
}
