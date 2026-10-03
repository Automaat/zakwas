// Package links symlinks repo files into the home directory, for configs
// that apps must be able to write. Everything else belongs in files.
package links

import (
	"context"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

type Module struct {
	Links []config.Link
	Paths config.Paths
}

func (m *Module) Name() string { return "links" }

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	var changes []engine.Change
	for _, l := range m.Links {
		c, err := install.PlanLink(m.Paths, m.Paths.Src(l.Src), m.Paths.Dst(l.Dst))
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return changes, nil
}
