package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/proposal"
)

func TestRateTreatsARelocatedAgentHomeAsSensitive(t *testing.T) {
	home := t.TempDir()
	copilot, codex := filepath.Join(t.TempDir(), "cop"), filepath.Join(t.TempDir(), "cdx")
	for _, tt := range []struct {
		name string
		env  Env
		dir  string
	}{
		{"copilot", Env{Home: home, CopilotHome: copilot}, copilot},
		{"codex", Env{Home: home, CodexHome: codex}, codex},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := filepath.Join(tt.dir, "hooks", "zakwas.json")
			plan := engine.Plan{{Module: "agents", Changes: []engine.Change{{Action: engine.Update, Target: target}}}}
			tier, reasons := rate(tt.env, []proposal.Op{{Kind: proposal.AddPlugin, Name: "a@b"}}, plan)
			if tier == "low" || !strings.Contains(strings.Join(reasons, "\n"), "the plan changes "+target) {
				t.Errorf("tier = %s, reasons = %q", tier, reasons)
			}
		})
	}
}
