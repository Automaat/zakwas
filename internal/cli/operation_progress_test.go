package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

type operationFake struct {
	runner.Runner
	run func(context.Context, runner.Cmd) (runner.Result, error)
}

func (r operationFake) Run(ctx context.Context, cmd runner.Cmd) (runner.Result, error) {
	return r.run(ctx, cmd)
}

type operationLines struct {
	mu sync.Mutex
	bytes.Buffer
	lines chan string
}

func (w *operationLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case w.lines <- string(p):
	default:
	}
	return w.Buffer.Write(p)
}

func TestUpgradeOperationVisibleWhileWaiting(t *testing.T) {
	output := &operationLines{lines: make(chan string, 20)}
	progress := &operationProgress{out: &console{w: output}, interval: time.Millisecond}
	release := make(chan struct{})
	fake := operationFake{Runner: runnertest.New(), run: func(context.Context, runner.Cmd) (runner.Result, error) {
		<-release
		return runner.Result{Stdout: "payload"}, nil
	}}
	r := &operationRunner{Runner: fake, progress: progress}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Run(withOperationPhase(context.Background(), "Plan"), runner.Cmd{Name: "codex", Args: []string{"plugin", "list", "--available", "--json"}})
	}()
	t.Cleanup(func() { close(release); <-done })

	for _, want := range []string{"Plan: codex plugin list --available --json: started", "Plan: codex plugin list --available --json: running", "Plan: codex plugin list --available --json: running"} {
		select {
		case got := <-output.lines:
			if !strings.Contains(got, want) {
				t.Errorf("line %q does not contain %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no operation update while command was blocked")
		}
	}
}

func TestUpgradeOperationPreservesOutcome(t *testing.T) {
	startErr := errors.New("cannot start")
	tests := []struct {
		name   string
		result runner.Result
		err    error
		want   string
	}{
		{"success", runner.Result{Stdout: "payload"}, nil, ": done ("},
		{"nonzero exit", runner.Result{ExitCode: 42, Stderr: "offline"}, nil, ": failed ("},
		{"start failure", runner.Result{}, startErr, ": failed ("},
		{"cancelled", runner.Result{}, context.Canceled, ": cancelled ("},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			progress := &operationProgress{out: &console{w: &output}, interval: time.Hour}
			fake := operationFake{Runner: runnertest.New(), run: func(context.Context, runner.Cmd) (runner.Result, error) { return tt.result, tt.err }}
			r := &operationRunner{Runner: fake, progress: progress}

			got, err := r.Run(withOperationPhase(context.Background(), "Refresh"), runner.Cmd{Name: "claude", Args: []string{"plugin", "marketplace", "update", "sai"}})
			progress.summary()

			if got != tt.result || !errors.Is(err, tt.err) {
				t.Errorf("outcome = %#v, %v; want %#v, %v", got, err, tt.result, tt.err)
			}
			if strings.Count(output.String(), tt.want) != 2 {
				t.Errorf("completion and summary missing result %q: %s", tt.want, &output)
			}
			if strings.ContainsAny(output.String(), "\x1b\r") {
				t.Errorf("redirected progress contains terminal controls: %q", &output)
			}
		})
	}
}

func TestUpgradeSummaryShowsSlowestFirst(t *testing.T) {
	var output bytes.Buffer
	progress := &operationProgress{out: &console{w: &output}, timings: []operationTiming{
		{"Refresh claude: sai", "done", time.Second},
		{"Plan codex: available plugins", "failed", 12 * time.Second},
	}}

	progress.summary()

	want := "Upgrade refresh and planning timings (slowest first):\n  Plan codex: available plugins: failed (12s)\n  Refresh claude: sai: done (1s)\n"
	if output.String() != want {
		t.Errorf("summary = %q, want %q", &output, want)
	}
}

func TestUpgradeWithoutExternalOperations(t *testing.T) {
	env, _ := setup(t, "agents: {guard: false, providers: [claude]}\n")

	r := invoke(env, "", "upgrade", "-y", "--only", "agents")

	if r.code != ExitOK || !strings.Contains(r.stderr, "No external operations.") {
		t.Errorf("empty upgrade: %+v", r)
	}
}

func TestUpgradeJSONHasNoHumanProgress(t *testing.T) {
	env, _ := setup(t, "agents: {guard: false, providers: [claude]}\n")

	r := invoke(env, "", "upgrade", "-y", "--only", "agents", "--json")

	if r.code != ExitOK || strings.Contains(r.stderr+r.stdout, "timings") || !strings.Contains(r.stdout, `"type":"plan"`) {
		t.Errorf("JSON upgrade: %+v", r)
	}
}

func TestApplyCommandsHaveNoUpgradeProgress(t *testing.T) {
	var output bytes.Buffer
	progress := &operationProgress{out: &console{w: &output}, interval: time.Hour}
	r := &operationRunner{Runner: runnertest.New().OnOK("tool", "payload"), progress: progress}

	got, err := r.Run(context.Background(), runner.Cmd{Name: "tool"})

	if err != nil || got.Stdout != "payload" || output.Len() != 0 {
		t.Errorf("apply command = %#v, %v, output %q", got, err, &output)
	}
}
