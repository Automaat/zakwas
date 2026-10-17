//go:build windows

package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func TestSkillsFailTheirProviderOnWindows(t *testing.T) {
	m, _ := newSkills(t, config.Agents{Skills: []config.Skill{{Path: "skills/mine"}}})
	localSkill(t, m, "skills/mine", "mine")

	changes, err := m.Plan(context.Background())

	if err == nil || !strings.Contains(err.Error(), "symlinks are not supported on Windows yet") || !strings.Contains(err.Error(), "claude: skills") {
		t.Fatalf("err = %v, want claude's skills to fail with the Windows message", err)
	}
	if len(changes) != 0 {
		t.Errorf("planned %v although the skill can't be linked", targets(changes))
	}
}

func TestGeminiExtensionLinkIsRefusedOnWindows(t *testing.T) {
	m := &Module{Paths: config.Paths{Home: t.TempDir()}, Runner: runnertest.New()}

	c, _, _, err := m.planGeminiLink(true)

	if c != nil || !errors.Is(err, install.ErrSymlinksUnsupported) {
		t.Errorf("planGeminiLink = %v, %v; want ErrSymlinksUnsupported", c, err)
	}
}
