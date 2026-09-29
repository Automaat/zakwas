// Package templates renders repo templates into protected copies, for configs
// that need machine values such as the home path.
package templates

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"text/template"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

const defaultMode fs.FileMode = 0o444

// Data is what templates can reference.
type Data struct {
	Home string
	Vars map[string]string
}

type Module struct {
	Templates config.Templates
	Paths     config.Paths
	Installer *install.Installer
}

func (m *Module) Name() string { return "templates" }

func (m *Module) Plan(_ context.Context) ([]engine.Change, error) {
	data := Data{Home: m.Paths.Home, Vars: m.Templates.Vars}
	var changes []engine.Change
	for _, f := range m.Templates.Files {
		mode := f.Mode
		if mode == 0 {
			mode = defaultMode
		}
		want, err := Render(m.Paths.Src(f.Src), data)
		if err != nil {
			return nil, err
		}
		c, err := m.Installer.Plan(m.Paths.Dst(f.Dst), want, mode)
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return changes, nil
}

// Render executes the template at path, failing on unknown variables.
func Render(path string, data Data) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := template.New(filepath.Base(path)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
