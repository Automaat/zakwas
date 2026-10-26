// Package layers keeps the registry of local config layers: extra
// zakwas.yaml files a machine merges into the config of one repo.
package layers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/install"
)

// Version is the registry document version.
const Version = 1

// Registry maps the real path of a main config root to the layers registered
// for it, in registration order.
type Registry struct {
	Version int                       `json:"version"`
	Roots   map[string][]config.Layer `json:"roots"`
}

// Path is where the registry lives.
func Path(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "layers.json")
}

// Load reads the registry; a missing file is an empty one.
func Load(home string) (*Registry, error) {
	path := Path(home)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Registry{Version: Version, Roots: map[string][]config.Layer{}}, nil
	}
	if err != nil {
		return nil, err
	}
	r := &Registry{}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if r.Version != Version {
		return nil, fmt.Errorf("%s: version %d is not supported by this zakwas (want %d); upgrade zakwas", path, r.Version, Version)
	}
	if r.Roots == nil {
		r.Roots = map[string][]config.Layer{}
	}
	return r, nil
}

// For lists the layers registered for the config root.
func (r *Registry) For(root string) []config.Layer {
	return slices.Clone(r.Roots[root])
}

// Set replaces the layers registered for the config root; none removes its
// entry.
func (r *Registry) Set(root string, layers []config.Layer) {
	if len(layers) == 0 {
		delete(r.Roots, root)
		return
	}
	r.Roots[root] = slices.Clone(layers)
}

// Save writes the registry atomically, so plan and check never read half of
// it.
func (r *Registry) Save(home string) error {
	r.Version = Version
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return install.WriteAtomic(Path(home), append(data, '\n'), 0o644)
}
