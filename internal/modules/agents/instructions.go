package agents

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

// instructionsState maps each instructions link zakwas made, by absolute
// path, to its provider and target, so a link of a provider no longer
// managed (or of an earlier config dir) is removed, and only zakwas's own.
type instructionsState struct {
	Links map[string]instructionsLink `json:"links"`
}

type instructionsLink struct {
	Provider string `json:"provider"`
	Target   string `json:"target"`
}

func (m *Module) instructionsStatePath() string {
	return filepath.Join(m.Paths.Home, ".local", "state", "zakwas", "instructions.json")
}

// instructionsPath is where a provider reads its global instructions.
// opencode's ignores OPENCODE_CONFIG_DIR, like its skills, and doesn't
// follow agents.opencode.skillsDir, which may be a shared skills dir.
func (m *Module) instructionsPath(provider string) string {
	switch provider {
	case config.ProviderClaude:
		return filepath.Join(m.claudeBackend().dir, "CLAUDE.md")
	case config.ProviderCodex:
		return filepath.Join(m.codexBackend().home, "AGENTS.md")
	default:
		return m.Paths.Dst("~/.config/opencode/AGENTS.md")
	}
}

func (m *Module) loadInstructions() (instructionsState, error) {
	st := instructionsState{}
	if err := loadState(m.instructionsStatePath(), &st); err != nil {
		return st, err
	}
	if st.Links == nil {
		st.Links = map[string]instructionsLink{}
	}
	return st, nil
}

func (m *Module) updateInstructions(edit func(*instructionsState)) error {
	st, err := m.loadInstructions()
	if err != nil {
		return err
	}
	edit(&st)
	return saveState(m.instructionsStatePath(), st)
}

// owner finds the state entry of link, ignoring case like macOS's file
// system.
func (st instructionsState) owner(link string) (string, instructionsLink, bool) {
	if l, ok := st.Links[link]; ok {
		return link, l, true
	}
	for key, l := range st.Links {
		if strings.EqualFold(key, link) {
			return key, l, true
		}
	}
	return "", instructionsLink{}, false
}

// planInstructions links agents.instructions to the global instructions
// of every managed provider, grouped by provider, and removes the links
// zakwas made that no provider wants anymore. Claude, and codex and
// opencode when zakwas.yaml names them, are strict: a link that can't be
// made fails the plan. Codex is opt-in, as for plugins, and opencode by
// the all-providers default is best effort: linked only when opencode is
// installed and its path is free to take, its link otherwise left as is.
func (m *Module) planInstructions() (map[string][]engine.Change, error) {
	statePath := m.instructionsStatePath()
	if m.Agents.Instructions == "" {
		if _, err := os.Lstat(statePath); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
	}
	if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("agents.instructions: %s is a symlink; remove it", m.Paths.Pretty(statePath))
	}
	if err := install.CheckParents(m.Paths, statePath); err != nil {
		return nil, fmt.Errorf("agents.instructions: %w", err)
	}
	st, err := m.loadInstructions()
	if err != nil {
		return nil, err
	}

	byProvider := map[string][]engine.Change{}
	keep := map[string]bool{}
	var errs []error
	if m.Agents.Instructions != "" {
		src := filepath.Clean(m.Paths.Src(m.Agents.Instructions))
		info, err := os.Stat(src)
		if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
			err = pe.Err
		}
		if err != nil {
			return nil, fmt.Errorf("agents.instructions: source %s: %w", m.Paths.Pretty(src), err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("agents.instructions: %s is a directory; point it at a file", src)
		}
		for _, p := range config.Providers {
			if !m.Agents.Manages(p) {
				continue
			}
			named := m.desired(p).named
			if p == config.ProviderCodex && !named {
				continue
			}
			strict := p == config.ProviderClaude || named
			dst := m.instructionsPath(p)
			if keep[strings.ToLower(dst)] {
				continue
			}
			keep[strings.ToLower(dst)] = true
			if !strict && !m.Runner.Installed(p) {
				continue
			}
			c, err := m.planInstruction(p, src, dst, st)
			if err != nil {
				if strict {
					errs = append(errs, fmt.Errorf("%s: instructions: %w", p, err))
				}
				continue
			}
			if c != nil {
				c.Group = p
				byProvider[p] = append(byProvider[p], *c)
			}
		}
	}

	for _, link := range slices.Sorted(maps.Keys(st.Links)) {
		if keep[strings.ToLower(link)] {
			continue
		}
		l := st.Links[link]
		c := m.planInstructionRemove(link, l)
		c.Group = l.Provider
		byProvider[l.Provider] = append(byProvider[l.Provider], c)
	}
	return byProvider, errors.Join(errs...)
}

// planInstruction links dst to src like the links module does, and
// records the link as zakwas's. A link someone else made to src is left
// alone and untracked.
func (m *Module) planInstruction(provider, src, dst string, st instructionsState) (*engine.Change, error) {
	c, err := install.PlanLink(m.Paths, src, dst)
	if err != nil {
		return nil, err
	}
	key, owned, ours := st.owner(dst)
	want := instructionsLink{Provider: provider, Target: src}
	record := func(st *instructionsState) {
		if ours {
			delete(st.Links, key)
		}
		st.Links[dst] = want
	}
	if c == nil {
		if !ours || key == dst && owned == want {
			return nil, nil
		}
		return &engine.Change{
			Action: engine.Update, Target: m.Paths.Pretty(dst), Detail: "track as " + provider + " instructions",
			Apply: func(context.Context) error { return m.updateInstructions(record) },
		}, nil
	}
	link := c.Apply
	c.Apply = func(ctx context.Context) error {
		if err := link(ctx); err != nil {
			return err
		}
		return m.updateInstructions(record)
	}
	return c, nil
}

// planInstructionRemove removes a link zakwas made; one that was changed
// since, or sits under a symlinked parent now, is only forgotten.
func (m *Module) planInstructionRemove(link string, l instructionsLink) engine.Change {
	forget := func(st *instructionsState) { delete(st.Links, link) }
	current, err := os.Readlink(link)
	if err != nil || current != l.Target || install.CheckParents(m.Paths, link) != nil {
		return engine.Change{
			Action: engine.Remove, Target: m.Paths.Pretty(link), Detail: "no longer zakwas's link, forget it",
			Apply: func(context.Context) error { return m.updateInstructions(forget) },
		}
	}
	return engine.Change{
		Action: engine.Remove, Target: m.Paths.Pretty(link), Detail: l.Provider + " instructions", Destructive: true,
		Apply: func(context.Context) error {
			if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return m.updateInstructions(forget)
		},
	}
}
