package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
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

	var progress []string
	err := Apply(context.Background(), func(s string) { progress = append(progress, s) }, plan)

	if got, want := strings.Join(ran, ","), "a1,a2,b1"; got != want {
		t.Errorf("ran %s, want %s (module stops at first failure, others continue)", got, want)
	}
	if err == nil || !strings.Contains(err.Error(), "a: a2: fail") {
		t.Errorf("err = %v", err)
	}
	if got, want := strings.Join(progress, "|"), "a: ! a1|a: ! a2|b: ! b1"; got != want {
		t.Errorf("progress %s, want %s", got, want)
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

	err := Apply(ctx, func(string) {}, plan)

	if got := strings.Join(ran, ","); got != "sudo" {
		t.Errorf("ran %s; nothing may run after cancellation", got)
	}
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "system: sudo: interrupted") {
		t.Errorf("err = %v, want the change error joined with context.Canceled", err)
	}
}

func TestPrint(t *testing.T) {
	plan := Plan{
		{Module: "links", Changes: []Change{{Action: Create, Target: "~/.zshrc", Detail: "→ x", Diff: "-old\n+new\n"}}},
		{Module: "brew"},
		{Module: "mise", Err: errors.New("mise not found")},
	}
	tests := []struct {
		diffs bool
		want  string
	}{
		{false, "links:\n  + ~/.zshrc (→ x)\nbrew: up to date\nmise: plan failed: mise not found\n"},
		{true, "links:\n  + ~/.zshrc (→ x)\n      -old\n      +new\nbrew: up to date\nmise: plan failed: mise not found\n"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		if err := Print(&out, plan, tt.diffs); err != nil || out.String() != tt.want {
			t.Errorf("diffs=%v got:\n%s\nwant:\n%s", tt.diffs, out.String(), tt.want)
		}
	}
}

func TestPlanCount(t *testing.T) {
	p := Plan{{Changes: []Change{{}, {}}}, {}, {Changes: []Change{{}}}}
	if p.Count() != 3 || p.Empty() {
		t.Errorf("Count = %d, Empty = %v", p.Count(), p.Empty())
	}
	if !(Plan{{Module: "x"}}).Empty() {
		t.Error("plan without changes should be empty")
	}
}
