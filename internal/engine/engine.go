// Package engine plans and applies changes across modules.
package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Action classifies a change for display.
type Action string

const (
	Create Action = "+"
	Update Action = "~"
	Remove Action = "-"
	Run    Action = "!"
)

// Change is one difference between desired and actual state. Apply is nil for
// changes that are only informational and applied by a later Run change of
// the same module (e.g. every missing brew entry is installed by one
// `brew bundle install`).
type Change struct {
	Action Action
	Target string
	Detail string
	// From and To are the old and new value or version, e.g. a tool bump.
	From, To string
	// Diff is shown with --diff: file content changes, command scripts.
	Diff string
	// Destructive marks changes that delete state the repo can't restore:
	// removed files, brew cleanup, mise prune.
	Destructive bool
	// Streams marks changes whose Apply passes a tool's output through.
	Streams bool
	Apply   func(ctx context.Context) error
}

// Step reports whether the change runs something itself, as opposed to only
// listing what a later step of its module does.
func (c Change) Step() bool { return c.Apply != nil }

// Summary joins From → To and Detail.
func (c Change) Summary() string {
	var parts []string
	if c.From != "" || c.To != "" {
		parts = append(parts, strings.TrimSpace(c.From+" → "+c.To))
	}
	if c.Detail != "" {
		parts = append(parts, c.Detail)
	}
	return strings.Join(parts, ", ")
}

func (c Change) String() string {
	if s := c.Summary(); s != "" {
		return fmt.Sprintf("%s %s (%s)", c.Action, c.Target, s)
	}
	return fmt.Sprintf("%s %s", c.Action, c.Target)
}

// Module computes the changes needed to converge one area of the system.
type Module interface {
	Name() string
	Plan(ctx context.Context) ([]Change, error)
}

// ModulePlan is the plan for one module. Err is set when the module could
// not be planned; its changes are then unknown and nothing is applied for it.
type ModulePlan struct {
	Module  string
	Changes []Change
	Err     error
}

// Plan is the full set of pending changes, in module order.
type Plan []ModulePlan

// Empty reports whether nothing needs to change.
func (p Plan) Empty() bool {
	return p.Count() == 0
}

// Count returns the number of changes across all modules.
func (p Plan) Count() int {
	n := 0
	for _, mp := range p {
		n += len(mp.Changes)
	}
	return n
}

// Steps returns the number of changes that run something.
func (p Plan) Steps() int {
	n := 0
	for _, mp := range p {
		for _, c := range mp.Changes {
			if c.Step() {
				n++
			}
		}
	}
	return n
}

// Counts is the number of changes per action, plus how many are destructive.
type Counts struct {
	Create, Update, Remove, Run, Destructive int
}

func (p Plan) Counts() Counts {
	var n Counts
	for _, mp := range p {
		for _, c := range mp.Changes {
			switch c.Action {
			case Create:
				n.Create++
			case Update:
				n.Update++
			case Remove:
				n.Remove++
			case Run:
				n.Run++
			}
			if c.Destructive {
				n.Destructive++
			}
		}
	}
	return n
}

// Err joins the planning errors of all modules.
func (p Plan) Err() error {
	var errs []error
	for _, mp := range p {
		if mp.Err != nil {
			errs = append(errs, fmt.Errorf("plan %s: %w", mp.Module, mp.Err))
		}
	}
	return errors.Join(errs...)
}

// Build plans every module. A module that fails to plan is recorded and the
// rest still plan, so one broken module (a brew network error, mise not yet
// installed) doesn't block converging everything else.
func Build(ctx context.Context, modules []Module) Plan {
	plan := make(Plan, 0, len(modules))
	for _, m := range modules {
		changes, err := m.Plan(ctx)
		plan = append(plan, ModulePlan{Module: m.Name(), Changes: changes, Err: err})
	}
	return plan
}

// Step identifies one change being applied: its position among the plan's
// steps and its module.
type Step struct {
	N, Total int
	Module   string
	Change   Change
}

// Observer is told about every step as it starts and finishes.
type Observer interface {
	Start(s Step)
	Done(s Step, d time.Duration, err error)
}

// Result counts what an apply did; steps after a module's first failure, or
// after cancellation, are skipped.
type Result struct {
	Applied, Failed, Skipped int
	Duration                 time.Duration
}

// Apply executes the plan in order, reporting each step to obs. Within a
// module the first failure stops that module, since later changes often
// depend on earlier ones; other modules still run and all failures are
// returned together. Cancelling ctx stops before the next change.
func Apply(ctx context.Context, obs Observer, p Plan) (Result, error) {
	start := time.Now()
	res := Result{}
	total := p.Steps()
	n := 0
	var errs []error
	for _, mp := range p {
		failed := false
		for _, c := range mp.Changes {
			if !c.Step() {
				continue
			}
			n++
			if failed || ctx.Err() != nil {
				res.Skipped++
				continue
			}
			step := Step{N: n, Total: total, Module: mp.Module, Change: c}
			obs.Start(step)
			t := time.Now()
			err := c.Apply(ctx)
			obs.Done(step, time.Since(t), err)
			if err != nil {
				res.Failed++
				failed = true
				errs = append(errs, fmt.Errorf("%s: %s: %w", mp.Module, c.Target, err))
				continue
			}
			res.Applied++
		}
	}
	res.Duration = time.Since(start)
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}
