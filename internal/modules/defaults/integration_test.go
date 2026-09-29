//go:build integration

package defaults

import (
	"context"
	"os/exec"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
)

const domain = "dev.zakwas.integration-test"

// TestRealDefaults round-trips every supported type through the real
// `defaults` CLI, proving Encode matches what `defaults read` prints.
func TestRealDefaults(t *testing.T) {
	if _, err := exec.LookPath("defaults"); err != nil {
		t.Skip("defaults CLI not available")
	}
	cleanup := func() {
		_ = exec.Command("defaults", "delete", domain).Run()
		_ = exec.Command("defaults", "-currentHost", "delete", domain).Run()
	}
	cleanup()
	t.Cleanup(cleanup)

	m := &Module{Runner: runner.NewExec(), Defaults: []config.Default{
		{Domain: domain, Key: "b", Value: true},
		{Domain: domain, Key: "i", Value: 15},
		{Domain: domain, Key: "f", Value: 0.25},
		{Domain: domain, Key: "s", Value: "~/Documents/screenshots"},
		{Domain: domain, Key: "h", Value: 1, CurrentHost: true},
	}}
	ctx := context.Background()

	changes, err := m.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 5 {
		t.Fatalf("fresh domain: %v", changes)
	}
	if err := enginetest.Apply(ctx, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if again, err := m.Plan(ctx); err != nil || len(again) != 0 {
		t.Fatalf("not idempotent: %v, %v", again, err)
	}

	if out, err := exec.Command("defaults", "-currentHost", "read", domain, "h").Output(); err != nil || string(out) != "1\n" {
		t.Errorf("currentHost value not in ByHost prefs: %q, %v", out, err)
	}
	if err := exec.Command("defaults", "read", domain, "h").Run(); err == nil {
		t.Error("currentHost value leaked into the any-host domain")
	}

	if err := exec.Command("defaults", "write", domain, "i", "-string", "15").Run(); err != nil {
		t.Fatal(err)
	}
	drift, err := m.Plan(ctx)
	if err != nil || len(drift) != 1 || drift[0].Summary() != "string:15 → int:15" {
		t.Errorf("type drift not detected: %v, %v", drift, err)
	}
}
