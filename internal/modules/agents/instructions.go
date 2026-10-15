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
// path, to its provider and target. Instructions are rendered copies now;
// this is only what an earlier zakwas linked, which is replaced by a copy,
// or removed when no provider wants it anymore, and only zakwas's own.
type instructionsState struct {
	Links map[string]instructionsLink `json:"links"`
}

type instructionsLink struct {
	Provider string `json:"provider"`
	Target   string `json:"target"`
}

// InstructionsStatePath is where an earlier zakwas recorded the
// instructions links it made under home.
func InstructionsStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "instructions.json")
}

// InstructionFilesStatePath is where zakwas records the hashes of the
// rendered instructions copies it wrote under home.
func InstructionFilesStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "instruction-files.json")
}

func (m *Module) instructionsStatePath() string { return InstructionsStatePath(m.Paths.Home) }

func (m *Module) instructionFilesStatePath() string { return InstructionFilesStatePath(m.Paths.Home) }

// instructionInstaller writes the rendered copies, with a state of its own
// apart from the files module's, which would remove them as orphans.
func (m *Module) instructionInstaller() *install.Installer {
	if m.installer == nil {
		m.installer = &install.Installer{Paths: m.Paths, StatePath: m.instructionFilesStatePath(), Immutable: m.Immutable}
	}
	return m.installer
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
	case sharedInstructions:
		return m.Paths.Dst("~/.agents/AGENTS.md")
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
	if len(st.Links) == 0 {
		if err := os.Remove(m.instructionsStatePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveState(m.instructionsStatePath(), st)
}

// owner finds the state entry of link, by the same file: ignoring case
// like macOS's file system and resolving symlinked parents, so the same
// config dir reached by another path is still zakwas's.
func (st instructionsState) owner(link string) (string, instructionsLink, bool) {
	if l, ok := st.Links[link]; ok {
		return link, l, true
	}
	for key, l := range st.Links {
		if samePath(key) == samePath(link) {
			return key, l, true
		}
	}
	return "", instructionsLink{}, false
}

// samePath keys a path by its resolved parent dir, ignoring case.
func samePath(path string) string {
	dir := filepath.Dir(path)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return strings.ToLower(filepath.Join(dir, filepath.Base(path)))
}

// planInstructions renders agents.instructions into the global
// instructions of every managed provider, grouped by provider, plus the
// shared ~/.agents/AGENTS.md when asked for, and removes what zakwas wrote
// that nothing wants anymore. Claude, and codex and opencode when
// zakwas.yaml names them, are strict: a copy that can't be rendered or
// written fails the plan. Codex is opt-in, as for plugins, and opencode by
// the all-providers default is best effort: written only when opencode is
// installed and nothing blocks it, its file otherwise left as is. A strict
// provider that can't be written is returned in failed. Links an earlier
// zakwas made are replaced by the copy, without a backup, or removed.
func (m *Module) planInstructions() (map[string][]engine.Change, map[string]error, error) {
	files := m.Agents.Instructions
	if len(files) == 0 {
		_, legacyErr := os.Lstat(m.instructionsStatePath())
		_, copiesErr := os.Lstat(m.instructionFilesStatePath())
		if errors.Is(legacyErr, fs.ErrNotExist) && errors.Is(copiesErr, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	for _, statePath := range []string{m.instructionsStatePath(), m.instructionFilesStatePath()} {
		if info, err := os.Lstat(statePath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("agents.instructions: %s is a symlink; remove it", m.Paths.Pretty(statePath))
		}
		if err := install.CheckParents(m.Paths, statePath); err != nil {
			return nil, nil, fmt.Errorf("agents.instructions: %w", err)
		}
	}
	st, err := m.loadInstructions()
	if err != nil {
		return nil, nil, err
	}
	in := m.instructionInstaller()
	tracked, err := in.Tracked()
	if err != nil {
		return nil, nil, err
	}

	recorded := map[string]string{}
	for _, dst := range tracked {
		recorded[samePath(dst)] = dst
	}
	byProvider := map[string][]engine.Change{}
	keep := map[string]bool{}
	failed := map[string]error{}
	for _, file := range files {
		src := filepath.Clean(m.Paths.Src(file))
		info, err := os.Stat(src)
		if err != nil {
			return nil, nil, fmt.Errorf("agents.instructions: source %s: %w", m.Paths.Pretty(src), pathErr(err))
		}
		if info.IsDir() {
			return nil, nil, fmt.Errorf("agents.instructions: %s is a directory; point it at a file", src)
		}
	}
	if len(files) > 0 {
		for _, p := range append(slices.Clone(config.Providers), sharedInstructions) {
			strict := true
			switch {
			case p == sharedInstructions:
				if !m.Agents.SharedInstructions {
					continue
				}
			case !m.Agents.Manages(p):
				continue
			default:
				named := m.desired(p).named
				if p == config.ProviderCodex && !named {
					continue
				}
				strict = p == config.ProviderClaude || named
			}
			dst := m.instructionsPath(p)
			if keep[samePath(dst)] {
				continue
			}
			keep[samePath(dst)] = true
			if !strict && !m.Runner.Installed(p) {
				continue
			}
			if known, ok := recorded[samePath(dst)]; ok {
				dst = known
			}
			want, err := m.renderInstructions(p)
			if err != nil {
				failed[p] = fmt.Errorf("%s: instructions: %w", p, err)
				continue
			}
			changes, err := m.planInstruction(in, st, dst, want)
			if err != nil {
				if strict {
					failed[p] = fmt.Errorf("%s: instructions: %w", p, err)
				}
				continue
			}
			for _, c := range changes {
				c.Group = p
				byProvider[p] = append(byProvider[p], c)
			}
		}
	}

	for _, dst := range tracked {
		if keep[samePath(dst)] {
			continue
		}
		c, err := m.planInstructionCopyRemove(in, dst)
		if err != nil {
			return nil, nil, err
		}
		c.Group = orphanInstructions
		byProvider[orphanInstructions] = append(byProvider[orphanInstructions], c)
	}
	for _, link := range slices.Sorted(maps.Keys(st.Links)) {
		if keep[samePath(link)] {
			continue
		}
		l := st.Links[link]
		c := m.planInstructionRemove(link, l)
		c.Group = l.Provider
		byProvider[l.Provider] = append(byProvider[l.Provider], c)
	}
	return byProvider, failed, nil
}

// orphanInstructions groups the removals of copies no provider wants, whose
// provider zakwas doesn't record.
const orphanInstructions = "instructions"

// planInstruction makes dst a protected copy of want. A link an earlier
// zakwas made there is replaced without a backup and forgotten.
func (m *Module) planInstruction(in *install.Installer, st instructionsState, dst string, want []byte) ([]engine.Change, error) {
	c, err := in.Plan(dst, want, 0o644)
	if err != nil {
		return nil, err
	}
	key, _, ours := st.owner(dst)
	forget := func(st *instructionsState) { delete(st.Links, key) }
	switch {
	case c == nil && !ours:
		return nil, nil
	case c == nil:
		return []engine.Change{{
			Action: engine.Update, Target: m.Paths.Pretty(dst), Detail: "no longer a link, stop tracking it as one",
			Apply: func(context.Context) error { return m.updateInstructions(forget) },
		}}, nil
	case ours:
		write := c.Apply
		c.Apply = func(ctx context.Context) error {
			if err := write(ctx); err != nil {
				return err
			}
			return m.updateInstructions(forget)
		}
	}
	return []engine.Change{*c}, nil
}

// symlinkedParent reports whether dst sits under a symlinked parent zakwas
// refuses to write through.
func (m *Module) symlinkedParent(dst string) bool {
	return install.CheckParents(m.Paths, dst) != nil
}

// planInstructionCopyRemove removes a copy zakwas wrote that no provider
// wants anymore. One edited since is backed up first; one that is no longer
// a regular file, or sits under a symlinked parent now, is only forgotten.
func (m *Module) planInstructionCopyRemove(in *install.Installer, dst string) (engine.Change, error) {
	target := m.Paths.Pretty(dst)
	forget := func() error {
		if err := in.State.Forget(dst); err != nil {
			return err
		}
		if len(in.State.Files) > 0 {
			return nil
		}
		if err := os.Remove(m.instructionFilesStatePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	forgetOnly := func(detail string) engine.Change {
		return engine.Change{Action: engine.Remove, Target: target, Detail: detail, Apply: func(context.Context) error { return forget() }}
	}
	throughSymlink := m.symlinkedParent(dst)
	info, err := os.Lstat(dst)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return forgetOnly("already gone, forget"), nil
	case err != nil:
		return engine.Change{}, err
	case !info.Mode().IsRegular() || throughSymlink:
		return forgetOnly("no longer zakwas's file, forget it"), nil
	}
	have, err := os.ReadFile(dst)
	if err != nil {
		return engine.Change{}, err
	}
	if recorded, _ := in.State.Get(dst); recorded != install.Sum(have) {
		return engine.Change{
			Action: engine.Remove, Target: target,
			Detail: "no longer managed, edited: back up to " + filepath.Base(install.BackupName(dst)),
			Apply: func(context.Context) error {
				if _, err := install.Backup(dst); err != nil {
					return err
				}
				return forget()
			},
		}, nil
	}
	return engine.Change{
		Action: engine.Remove, Target: target, Detail: "instructions no longer managed", Destructive: true,
		Apply: func(context.Context) error {
			if err := install.Unlock(dst); err != nil {
				return err
			}
			if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return forget()
		},
	}, nil
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
	detail := l.Provider + " instructions"
	if _, err := os.Lstat(link + ".zakwas-bak"); err == nil {
		detail += "; your earlier file stays at " + filepath.Base(link) + ".zakwas-bak"
	}
	return engine.Change{
		Action: engine.Remove, Target: m.Paths.Pretty(link), Detail: detail, Destructive: true,
		Apply: func(context.Context) error {
			if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return m.updateInstructions(forget)
		},
	}
}
