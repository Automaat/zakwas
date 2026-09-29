// Package engine plans and applies changes across modules.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	Diff   string
	Apply  func(ctx context.Context) error
}

func (c Change) String() string {
	if c.Detail == "" {
		return fmt.Sprintf("%s %s", c.Action, c.Target)
	}
	return fmt.Sprintf("%s %s (%s)", c.Action, c.Target, c.Detail)
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

// Print writes a human-readable plan; with diffs it also shows the content
// change of every file.
func Print(w io.Writer, p Plan, diffs bool) error {
	var b strings.Builder
	for _, mp := range p {
		switch {
		case mp.Err != nil:
			fmt.Fprintf(&b, "%s: plan failed: %v\n", mp.Module, mp.Err)
			continue
		case len(mp.Changes) == 0:
			fmt.Fprintf(&b, "%s: up to date\n", mp.Module)
			continue
		}
		fmt.Fprintf(&b, "%s:\n", mp.Module)
		for _, c := range mp.Changes {
			fmt.Fprintf(&b, "  %s\n", c)
			if diffs && c.Diff != "" {
				for line := range strings.Lines(c.Diff) {
					fmt.Fprintf(&b, "      %s", line)
				}
				if !strings.HasSuffix(c.Diff, "\n") {
					b.WriteString("\n")
				}
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Apply executes the plan in order, reporting each change to progress before
// running it. Within a module the first failure stops that module, since
// later changes often depend on earlier ones; other modules still run and all
// failures are returned together. Cancelling ctx stops before the next
// change.
func Apply(ctx context.Context, progress func(string), p Plan) error {
	var errs []error
	for _, mp := range p {
		for _, c := range mp.Changes {
			if c.Apply == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			progress(mp.Module + ": " + c.String())
			if err := c.Apply(ctx); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %w", mp.Module, c.Target, err))
				break
			}
		}
	}
	return errors.Join(errs...)
}
