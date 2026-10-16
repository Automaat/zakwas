//go:build windows

package agents

import (
	"context"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
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
