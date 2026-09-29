package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type stubModule struct {
	name    string
	changes []Change
	err     error
}

func (s stubModule) Name() string                           { return s.name }
func (s stubModule) Plan(context.Context) ([]Change, error) { return s.changes, s.err }

func TestBuildKeepsPlanningAfterAFailure(t *testing.T) {
	plan := Build(context.Background(), []Module{
		stubModule{name: "bad", err: errors.New("boom")},
		stubModule{name: "ok", changes: []Change{{Action: Create, Target: "x"}}},
	})
	if len(plan) != 2 || plan[1].Module != "ok" || len(plan[1].Changes) != 1 {
		t.Fatalf("plan = %+v; a failing module must not stop the next one", plan)
	}
	if err := plan.Err(); err == nil || !strings.Contains(err.Error(), "plan bad: boom") {
		t.Errorf("Err = %v", err)
	}
	if plan.Count() != 1 {
		t.Errorf("Count = %d", plan.Count())
	}
}

type recorder struct{ events []string }

func (r *recorder) Start(s Step) {
	r.events = append(r.events, fmt.Sprintf("start %d/%d %s %s", s.N, s.Total, s.Module, s.Change.Target))
}

func (r *recorder) Done(s Step, _ time.Duration, err error) {
	r.events = append(r.events, fmt.Sprintf("done %s %v", s.Change.Target, err != nil))
}

func TestApply(t *testing.T) {
	var ran []string
	record := func(name string, err error) func(context.Context) error {
		return func(context.Context) error {
			ran = append(ran, name)
			return err
		}
	}
	plan := Plan{
		{Module: "a", Changes: []Change{
			{Action: Create, Target: "info-only"},
			{Action: Run, Target: "a1", Apply: record("a1", nil)},
			{Action: Run, Target: "a2", Apply: record("a2", errors.New("fail"))},
			{Action: Run, Target: "a3", Apply: record("a3", nil)},
		}},
		{Module: "b", Changes: []Change{
			{Action: Run, Target: "b1", Apply: record("b1", nil)},
		}},
	}

	obs := &recorder{}
	res, err := Apply(context.Background(), obs, plan)

	if got, want := strings.Join(ran, ","), "a1,a2,b1"; got != want {
		t.Errorf("ran %s, want %s (module stops at first failure, others continue)", got, want)
	}
	if err == nil || !strings.Contains(err.Error(), "a: a2: fail") {
		t.Errorf("err = %v", err)
	}
	want := []string{
		"start 1/4 a a1", "done a1 false",
		"start 2/4 a a2", "done a2 true",
		"start 4/4 b b1", "done b1 false",
	}
	if !slices.Equal(obs.events, want) {
		t.Errorf("events %v, want %v", obs.events, want)
	}
	if res.Applied != 2 || res.Failed != 1 || res.Skipped != 1 {
		t.Errorf("result %+v, want 2 applied, 1 failed, 1 skipped", res)
	}
}

func TestApplyStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ran []string
	plan := Plan{
		{Module: "system", Changes: []Change{
			{Action: Run, Target: "sudo", Apply: func(context.Context) error {
				ran = append(ran, "sudo")
				cancel()
				return errors.New("interrupted")
			}},
		}},
		{Module: "files", Changes: []Change{
			{Action: Create, Target: "f", Apply: func(context.Context) error {
				ran = append(ran, "f")
				return nil
			}},
		}},
	}

	res, err := Apply(ctx, &recorder{}, plan)

	if got := strings.Join(ran, ","); got != "sudo" {
		t.Errorf("ran %s; nothing may run after cancellation", got)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "system: sudo: interrupted") {
		t.Errorf("err = %v, want the change error joined with context.Canceled", err)
	}
	if res.Skipped != 1 {
		t.Errorf("result %+v, want the cancelled step skipped", res)
	}
}

func TestRender(t *testing.T) {
	plan := Plan{
		{Module: "links", Changes: []Change{
			{Action: Create, Target: "~/.zshrc", Detail: "→ x", Diff: "-old\n+new\n"},
			{Action: Remove, Target: "~/.old", Detail: "no longer managed", Destructive: true},
		}},
		{Module: "mise", Changes: []Change{
			{Action: Update, Target: "ruff", From: "0.16.2", To: "0.16.9"},
			{Action: Run, Target: "mise install", Apply: func(context.Context) error { return nil }},
		}},
		{Module: "brew"},
		{Module: "defaults"},
		{Module: "system", Err: errors.New("boom")},
	}
	tests := []struct {
		name  string
		style Style
		want  string
	}{
		{"plain", Style{}, `links
  + ~/.zshrc  → x
  - ~/.old    no longer managed
mise
  ~ ruff          0.16.2 → 0.16.9
  ▶ mise install
✗ system: plan failed: boom
✓ up to date: brew, defaults

⚠ destructive: ~/.old

Plan: 1 to add, 1 to change, 1 to remove, 1 to run.
`},
		{"diffs", Style{ShowDiffs: true}, `links
  + ~/.zshrc  → x
      -old
      +new
  - ~/.old    no longer managed
mise
  ~ ruff          0.16.2 → 0.16.9
  ▶ mise install
✗ system: plan failed: boom
✓ up to date: brew, defaults

⚠ destructive: ~/.old

Plan: 1 to add, 1 to change, 1 to remove, 1 to run.
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Render(&out, plan, tt.style); err != nil || out.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tt.want)
			}
		})
	}
}

func TestRenderColorOnlyWhenAsked(t *testing.T) {
	plan := Plan{{Module: "m", Changes: []Change{{Action: Create, Target: "x"}}}}
	var plain, colored bytes.Buffer
	_ = Render(&plain, plan, Style{})
	_ = Render(&colored, plan, Style{Color: true})
	if strings.Contains(plain.String(), "\033[") {
		t.Errorf("escape codes without color: %q", plain.String())
	}
	if !strings.Contains(colored.String(), "\033[32m+") {
		t.Errorf("no green + with color: %q", colored.String())
	}
}

func TestRenderSummaryWithoutChanges(t *testing.T) {
	for _, tt := range []struct {
		plan Plan
		want string
	}{
		{Plan{{Module: "a"}}, "No changes. The machine matches the config."},
		{Plan{{Module: "a", Err: errors.New("x")}}, "No changes planned; some modules failed to plan."},
	} {
		var out bytes.Buffer
		_ = Render(&out, tt.plan, Style{})
		if !strings.Contains(out.String(), tt.want) {
			t.Errorf("got %q, want %q", out.String(), tt.want)
		}
	}
}

func TestDestructiveListIsCapped(t *testing.T) {
	var changes []Change
	for i := range 7 {
		changes = append(changes, Change{Action: Remove, Target: strconv.Itoa(i), Destructive: true})
	}
	got := destructive(Plan{{Changes: changes}})
	if want := []string{"0", "1", "2", "3", "4", "2 more"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestPlanCounts(t *testing.T) {
	step := func(context.Context) error { return nil }
	p := Plan{
		{Changes: []Change{{Action: Create}, {Action: Remove, Destructive: true}}},
		{},
		{Changes: []Change{{Action: Run, Apply: step}, {Action: Update, Apply: step}}},
	}
	if p.Count() != 4 || p.Empty() || p.Steps() != 2 {
		t.Errorf("Count = %d, Empty = %v, Steps = %d", p.Count(), p.Empty(), p.Steps())
	}
	if got, want := p.Counts(), (Counts{Create: 1, Update: 1, Remove: 1, Run: 1, Destructive: 1}); got != want {
		t.Errorf("Counts = %+v, want %+v", got, want)
	}
	if !(Plan{{Module: "x"}}).Empty() {
		t.Error("plan without changes should be empty")
	}
}

func TestChangeString(t *testing.T) {
	for c, want := range map[*Change]string{
		{Action: Create, Target: "a"}:                                  "+ a",
		{Action: Update, Target: "jq", From: "1", To: "2"}:             "~ jq (1 → 2)",
		{Action: Update, Target: "k", From: "1", To: "2", Detail: "x"}: "~ k (1 → 2, x)",
	} {
		if got := c.String(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
