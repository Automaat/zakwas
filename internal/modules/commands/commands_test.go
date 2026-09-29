package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

var (
	pass = runner.Result{}
	fail = runner.Result{ExitCode: 1}
)

func cmd() config.Command {
	return config.Command{Name: "gke-auth", Check: "command -v gke-gcloud-auth-plugin", Run: "gcloud components install gke-gcloud-auth-plugin"}
}

func run(t *testing.T, fake *runnertest.Fake) ([]engine.Change, error) {
	t.Helper()
	m := &Module{Runner: fake, Commands: []config.Command{cmd()}, Home: "/home/me"}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}})
	for _, c := range fake.Calls {
		if c.Dir != m.Home {
			t.Errorf("%s ran in %q, want $HOME", c, c.Dir)
		}
	}
	return changes, err
}

func TestSatisfiedIsSkipped(t *testing.T) {
	fake := runnertest.New().On("sh -c "+cmd().Check, pass)
	changes, err := run(t, fake)
	if err != nil || len(changes) != 0 {
		t.Errorf("changes = %v, err = %v", changes, err)
	}
}

func TestRunsUntilCheckPasses(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, pass).
		On("sh -c "+cmd().Run, pass)

	changes, err := run(t, fake)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes = %v, err = %v", changes, err)
	}
	if !fake.Ran("sh -c " + cmd().Run) {
		t.Error("run was not executed")
	}
}

func TestSatisfiedSincePlanning(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Check, pass)

	if _, err := run(t, fake); err != nil {
		t.Fatal(err)
	}
	if fake.Ran("sh -c " + cmd().Run) {
		t.Error("run must be skipped when an earlier module already satisfied the check")
	}
}

func TestStillFailingAfterRun(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Run, pass)

	_, err := run(t, fake)
	if err == nil || !strings.Contains(err.Error(), "still fails") {
		t.Errorf("err = %v", err)
	}
}

func TestRunFailure(t *testing.T) {
	fake := runnertest.New().
		On("sh -c "+cmd().Check, fail).
		On("sh -c "+cmd().Run, runner.Result{ExitCode: 2, Stderr: "network down"})

	_, err := run(t, fake)
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Errorf("err = %v", err)
	}
}

func TestHungCheckTimesOut(t *testing.T) {
	defer func(d time.Duration) { CheckTimeout = d }(CheckTimeout)
	CheckTimeout = 100 * time.Millisecond

	m := &Module{
		Runner:   runner.NewExec(),
		Home:     t.TempDir(),
		Commands: []config.Command{{Name: "hang", Check: "sleep 10", Run: "true"}},
	}
	start := time.Now()
	_, err := m.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hang: check timed out") {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("plan took %s; a hung check must not hang it", d)
	}
}
