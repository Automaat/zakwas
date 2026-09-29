// Package enginetest applies plans in tests without reporting progress.
package enginetest

import (
	"context"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
)

type discard struct{}

func (discard) Start(engine.Step)                      {}
func (discard) Done(engine.Step, time.Duration, error) {}

// Apply applies p and returns only the error.
func Apply(ctx context.Context, p engine.Plan) error {
	_, err := engine.Apply(ctx, discard{}, p)
	return err
}
