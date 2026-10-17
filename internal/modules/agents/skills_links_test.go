package agents

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func denyLinks(t *testing.T, junction func(target, link string) error) {
	t.Helper()
	t.Cleanup(install.SetLinkOS("windows",
		func(target, link string) error {
			return &os.LinkError{Op: "symlink", Old: target, New: link, Err: syscall.Errno(1314)}
		}, junction))
}

func TestSkillsFailTheirProviderWhenWindowsDeniesLinks(t *testing.T) {
	denyLinks(t, func(string, string) error { return errors.New("no junction either") })
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	localSkill(t, m, "skills/mine", "mine")

	changes, err := m.Plan(context.Background())

	if !errors.Is(err, install.ErrLinksDenied) || !strings.Contains(err.Error(), "claude: skills") {
		t.Fatalf("err = %v, want claude's skills to fail with the link message", err)
	}
	if strings.Count(err.Error(), "\n") != 0 {
		t.Errorf("the failure spans several lines: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("planned %v although the skill can't be linked", targets(changes))
	}
}

func TestSkillsPlanWhenOnlyAJunctionWorks(t *testing.T) {
	denyLinks(t, os.Symlink)
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	localSkill(t, m, "skills/mine", "mine")

	changes, err := m.Plan(context.Background())

	if err != nil || len(changes) == 0 {
		t.Fatalf("changes = %v, err = %v; directory links fall back to a junction", targets(changes), err)
	}
}

func TestGeminiExtensionLinkIsRefusedWhenWindowsDeniesLinks(t *testing.T) {
	denyLinks(t, func(string, string) error { return errors.New("no junction either") })
	m := &Module{Paths: config.Paths{Home: t.TempDir()}, Runner: runnertest.New()}

	c, _, _, err := m.planGeminiLink(true)

	if c != nil || !errors.Is(err, install.ErrLinksDenied) {
		t.Errorf("planGeminiLink = %v, %v; want ErrLinksDenied", c, err)
	}
}
