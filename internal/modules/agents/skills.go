package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
)

// skillsState records what zakwas cloned and linked for agents.skills: each
// clone by its cache dir name, and each link by absolute path, so a changed
// skills dir still finds the old ones. Only these are ever removed.
type skillsState struct {
	Sources map[string]string    `json:"sources"`
	Links   map[string]skillLink `json:"links"`
}

type skillLink struct {
	Target string `json:"target"`
	Skill  string `json:"skill"`
}

// SkillsStatePath is where zakwas records the skill links and git clones it
// made under home.
func SkillsStatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "skills.json")
}

func (m *Module) skillsStatePath() string { return SkillsStatePath(m.Paths.Home) }

func (m *Module) skillsCacheDir() string {
	return filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "skills")
}

// orphanSkills groups the removals of links and clones no skill wants.
const orphanSkills = "skills"

func (m *Module) loadSkills() (skillsState, error) {
	st := skillsState{}
	if err := loadState(m.skillsStatePath(), &st); err != nil {
		return st, err
	}
	if st.Sources == nil {
		st.Sources = map[string]string{}
	}
	if st.Links == nil {
		st.Links = map[string]skillLink{}
	}
	return st, nil
}

// updateSkills rewrites the state after each step, so an interrupted apply
// keeps track of every link and clone it made. The file goes with the last
// entry.
func (m *Module) updateSkills(edit func(*skillsState)) error {
	st, err := m.loadSkills()
	if err != nil {
		return err
	}
	edit(&st)
	if len(st.Sources) == 0 && len(st.Links) == 0 {
		if err := os.Remove(m.skillsStatePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return saveState(m.skillsStatePath(), st)
}

// owner finds the state entry of link, matching case-insensitively like
// macOS's file system, so a skill renamed only in case is still zakwas's.
func (st skillsState) owner(link string) (string, skillLink, bool) {
	if l, ok := st.Links[link]; ok {
		return link, l, true
	}
	for key, l := range st.Links {
		if strings.EqualFold(key, link) {
			return key, l, true
		}
	}
	return "", skillLink{}, false
}

// skillsDir is where a provider loads user skills from: Claude Code only
// from its own config dir, Codex, opencode and Copilot CLI from ~/.agents/skills, which
// GEMINI_CLI_HOME moves for Gemini and where Copilot reads only its own home's skills
// once $COPILOT_HOME moves it. opencode's own skillsDir, which holds plugin skills, is
// not used.
func (m *Module) skillsDir(provider string) string {
	switch {
	case provider == config.ProviderClaude:
		return filepath.Join(m.claudeBackend().dir, "skills")
	case provider == config.ProviderGemini && m.GeminiHome != "":
		return filepath.Join(m.GeminiHome, ".agents", "skills")
	case provider == config.ProviderCopilot && m.CopilotHome != "":
		return filepath.Join(m.CopilotHome, "skills")
	}
	return m.Paths.Dst("~/.agents/skills")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// skillSource is the git source of one or more skills, cloned once into the
// cache dir named id. src is 'owner/repo' or a URL, with '#ref' when set.
type skillSource struct {
	id  string
	src string
}

func newSkillSource(s config.Skill) skillSource {
	src := s.Git
	if s.Ref != "" {
		src += "#" + s.Ref
	}
	canon := canonical(src)
	sum := sha256.Sum256([]byte(canon))
	slug := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(canon), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[len(slug)-40:], "-")
	}
	return skillSource{id: slug + "-" + hex.EncodeToString(sum[:4]), src: src}
}

// skillWant is one link a provider should have: to the directory local for
// a skill on disk, or to subpath inside the clone of source.
type skillWant struct {
	link     string
	name     string
	provider string
	strict   bool
	local    string
	source   *skillSource
	subpath  string
}

func (m *Module) skillTarget(w skillWant) string {
	if w.source == nil {
		return w.local
	}
	return filepath.Join(m.skillsCacheDir(), w.source.id, w.subpath)
}

func (m *Module) skillWant(p, dir string, s config.NamedSkill, strict bool) skillWant {
	w := skillWant{link: filepath.Join(dir, s.Name), name: s.Name, provider: p, strict: strict}
	switch {
	case s.Git != "":
		src := newSkillSource(s.Skill)
		w.source, w.subpath = &src, filepath.Clean(s.Path)
	case config.IsHomePath(s.Path):
		w.local = m.Paths.Dst(s.Path)
	default:
		w.local = filepath.Clean(m.Paths.Src(s.Path))
	}
	return w
}

func (m *Module) skillDetail(w skillWant) string {
	if w.source != nil {
		return w.source.src + " " + filepath.ToSlash(w.subpath)
	}
	return "→ " + m.Paths.Pretty(w.local)
}

type providerSkills struct {
	provider string
	wants    []skillWant
}

// planSkills links agents.skills into every managed provider's skills
// directory and removes the links and clones zakwas made that nothing wants
// anymore. Claude, and codex, copilot and opencode when zakwas.yaml names them, are
// strict: a skill that can't be linked fails that provider. opencode by the
// all-providers default is best effort: skipped when it isn't installed or
// nothing can be linked. Codex and opencode share ~/.agents/skills, so a
// skill is linked there once.
//
// plan fetches nothing: git skills whose clone is missing plan a clone and,
// since their files are only known afterwards, one last step per provider
// that checks and links them.
func (m *Module) planSkills() (map[string][]engine.Change, map[string]error, error) {
	if len(m.Agents.Skills) == 0 {
		if _, err := os.Lstat(m.skillsStatePath()); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
	}
	if err := m.checkSkillsOwnDirs(); err != nil {
		return nil, nil, err
	}
	st, err := m.loadSkills()
	if err != nil {
		return nil, nil, err
	}

	wanted := map[string]bool{}
	declared := map[string]bool{}
	seen := map[string]bool{}
	var providers []providerSkills
	for _, p := range config.Providers {
		list := m.Agents.SkillsFor(p)
		if len(list) == 0 || !m.Agents.Manages(p) {
			continue
		}
		dir := m.skillsDir(p)
		strict := p == config.ProviderClaude || m.Agents.NamesInSkill(p)
		ps := providerSkills{provider: p}
		for _, s := range list {
			w := m.skillWant(p, dir, s, strict)
			wanted[strings.ToLower(w.link)] = true
			if w.source != nil {
				declared[w.source.id] = true
			}
			if !strict && !m.Runner.Installed(p) || seen[strings.ToLower(w.link)] {
				continue
			}
			seen[strings.ToLower(w.link)] = true
			ps.wants = append(ps.wants, w)
		}
		providers = append(providers, ps)
	}

	changes := map[string][]engine.Change{}
	failed := map[string]error{}
	for _, ps := range providers {
		out, err := m.planProviderSkills(ps, st)
		if err != nil {
			failed[ps.provider] = fmt.Errorf("%s: skills: %w", ps.provider, err)
			continue
		}
		changes[ps.provider] = out
	}

	for _, link := range slices.Sorted(maps.Keys(st.Links)) {
		if wanted[strings.ToLower(link)] {
			continue
		}
		c := m.planSkillRemove(link, st.Links[link])
		c.Group = orphanSkills
		changes[orphanSkills] = append(changes[orphanSkills], c)
	}
	for _, id := range slices.Sorted(maps.Keys(st.Sources)) {
		if declared[id] {
			continue
		}
		c := m.planSourceRemove(id, st.Sources[id])
		c.Group = orphanSkills
		changes[orphanSkills] = append(changes[orphanSkills], c)
	}
	return changes, failed, nil
}

// checkSkillsOwnDirs refuses zakwas's state and cache paths when they, or a
// directory above them, are a symlink under $HOME, which could point into
// the config repo.
func (m *Module) checkSkillsOwnDirs() error {
	if info, err := os.Lstat(m.skillsStatePath()); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("agents.skills: %s is a symlink; remove it", m.Paths.Pretty(m.skillsStatePath()))
	}
	for _, p := range []string{m.skillsStatePath(), filepath.Join(m.skillsCacheDir(), ".zakwas")} {
		if err := install.CheckParents(m.Paths, p); err != nil {
			return fmt.Errorf("agents.skills: %w", err)
		}
	}
	return nil
}

// planProviderSkills plans the links of one provider. A best-effort skill
// that can't be linked is skipped; any other fails the provider. Skills of a
// missing clone wait for the provider's last step, which clones before it
// links, so no provider depends on another's changes; their link paths are
// checked now all the same.
func (m *Module) planProviderSkills(ps providerSkills, st skillsState) ([]engine.Change, error) {
	var links []engine.Change
	var waiting []skillWant
	var errs []error
	fail := func(w skillWant, err error) {
		if w.strict {
			errs = append(errs, err)
		}
	}
	for _, w := range ps.wants {
		cloned := w.source == nil || m.fetched(st, *w.source)
		switch {
		case w.source == nil:
			if err := m.checkSkillDir(w, ""); err != nil {
				fail(w, err)
				continue
			}
		case cloned:
			if err := m.checkSkillDir(w, filepath.Join(m.skillsCacheDir(), w.source.id)); err != nil {
				fail(w, err)
				continue
			}
		case !m.Runner.Installed("git"):
			fail(w, fmt.Errorf("skill %q needs git to fetch %s, and git is not installed", w.name, w.source.src))
			continue
		}
		c, err := m.planSkillLink(w, st)
		if err != nil {
			fail(w, err)
			continue
		}
		if !cloned {
			waiting = append(waiting, w)
		} else if c != nil {
			links = append(links, *c)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	out := links
	if len(waiting) > 0 {
		out = append(out, m.planFetchedLinks(ps.provider, waiting, st))
	}
	for i := range out {
		out[i].Group = ps.provider
	}
	return out, nil
}

// planFetchedLinks is the last step of a provider with skills in clones that
// are missing: it clones them, checks every skill and links them.
func (m *Module) planFetchedLinks(provider string, waiting []skillWant, st skillsState) engine.Change {
	var clones, names []string
	seen := map[string]bool{}
	for _, w := range waiting {
		names = append(names, w.name)
		if !seen[w.source.id] {
			seen[w.source.id] = true
			clones = append(clones, w.source.src)
		}
	}
	action := engine.Create
	for _, w := range waiting {
		if _, ok := st.Sources[w.source.id]; ok {
			action = engine.Update
		}
	}
	return engine.Change{
		Action: action, Target: provider + " skills",
		Detail: "clone " + strings.Join(clones, ", ") + ", link " + strings.Join(names, ", "),
		Apply:  func(ctx context.Context) error { return m.linkFetched(ctx, waiting) },
	}
}

// fetched reports whether src was cloned by zakwas and is still there.
func (m *Module) fetched(st skillsState, s skillSource) bool {
	have, ok := st.Sources[s.id]
	if !ok || canonical(have) != canonical(s.src) {
		return false
	}
	info, err := os.Stat(filepath.Join(m.skillsCacheDir(), s.id, ".git"))
	return err == nil && info.IsDir()
}

// clone fetches s unless it is already cloned, and records it.
func (m *Module) clone(ctx context.Context, s skillSource) error {
	st, err := m.loadSkills()
	if err != nil {
		return err
	}
	if m.fetched(st, s) {
		return nil
	}
	if err := gitClone(ctx, m.Runner, m.skillsCacheDir(), s.id, s.src); err != nil {
		return fmt.Errorf("skills source %s: %w", s.src, err)
	}
	return m.updateSkills(func(st *skillsState) { st.Sources[s.id] = s.src })
}

// linkFetched clones what is missing, checks every skill that waited for a
// clone before linking any, so a bad one doesn't leave the others half done.
// Best-effort skills that can't be linked are skipped, as the plan does.
func (m *Module) linkFetched(ctx context.Context, waiting []skillWant) error {
	var errs []error
	cloned := map[string]bool{}
	for _, w := range waiting {
		if cloned[w.source.id] {
			continue
		}
		cloned[w.source.id] = true
		if err := m.clone(ctx, *w.source); err != nil && w.strict {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	st, err := m.loadSkills()
	if err != nil {
		return err
	}
	var ready []skillWant
	for _, w := range waiting {
		err := m.checkSkillDir(w, filepath.Join(m.skillsCacheDir(), w.source.id))
		if err == nil && !m.fetched(st, *w.source) {
			err = fmt.Errorf("skill %q: %s was not cloned", w.name, w.source.src)
		}
		switch {
		case err == nil:
			ready = append(ready, w)
		case w.strict:
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for _, w := range ready {
		c, err := m.planSkillLink(w, st)
		if err != nil {
			if w.strict {
				return err
			}
			continue
		}
		if c == nil {
			continue
		}
		if err := c.Apply(ctx); err != nil {
			return fmt.Errorf("%s: %w", c.Target, err)
		}
		if st, err = m.loadSkills(); err != nil {
			return err
		}
	}
	return nil
}

// checkSkillDir checks the directory a skill links to: it exists, holds a
// SKILL.md whose name is the directory's, and, in a clone, stays inside it.
func (m *Module) checkSkillDir(w skillWant, cloneRoot string) error {
	dir := m.skillTarget(w)
	label := m.Paths.Pretty(dir)
	if w.source != nil {
		label = w.source.src + " " + filepath.ToSlash(w.subpath)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("skill %q: %s does not exist", w.name, label)
	case err != nil:
		return fmt.Errorf("skill %q: %w", w.name, err)
	case !info.IsDir():
		return fmt.Errorf("skill %q: %s is not a directory", w.name, label)
	}
	if cloneRoot != "" {
		root, err := filepath.EvalSymlinks(cloneRoot)
		if err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || !within(real, root) {
			return fmt.Errorf("skill %q: %s resolves outside its repository", w.name, label)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("skill %q: %s has no SKILL.md", w.name, label)
	}
	if err != nil {
		return fmt.Errorf("skill %q: %w", w.name, err)
	}
	name, ok := skillMDName(data)
	switch {
	case !ok:
		return fmt.Errorf("skill %q: SKILL.md of %s has no name in its frontmatter", w.name, label)
	case name != w.name:
		return fmt.Errorf("skill %q: SKILL.md of %s names it %q; the directory and the name must match", w.name, label, name)
	}
	return nil
}

// skillMDName reads the name from the frontmatter of a SKILL.md. It scans
// for the line rather than parsing the YAML, which agents read leniently.
func skillMDName(data []byte) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) != "---" {
		return "", false
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			return v, v != ""
		}
	}
	return "", false
}

// planSkillLink links one skill. It replaces only links zakwas made; a link
// the user made to the same directory is left alone and untracked, and
// anything else at the path, such as an `npx skills` install, fails the plan.
func (m *Module) planSkillLink(w skillWant, st skillsState) (*engine.Change, error) {
	link, target := w.link, m.skillTarget(w)
	if err := install.CheckParents(m.Paths, link); err != nil {
		return nil, fmt.Errorf("skill %q: %w", w.name, err)
	}
	key, owned, ours := st.owner(link)
	entry := skillLink{Target: target, Skill: w.name}
	record := func(st *skillsState) {
		if ours {
			delete(st.Links, key)
		}
		st.Links[link] = entry
	}
	create := func(context.Context) error {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		if err := install.CreateLink(target, link); err != nil {
			return err
		}
		return m.updateSkills(record)
	}
	needLinks := func() error {
		if err := install.RequireLinks(true); err != nil {
			return fmt.Errorf("skill %q: linking it into %s: %w", w.name, m.Paths.Pretty(link), err)
		}
		return nil
	}
	label, detail := m.Paths.Pretty(link), m.skillDetail(w)
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := needLinks(); err != nil {
			return nil, err
		}
		return &engine.Change{Action: engine.Create, Target: label, Detail: detail, Apply: create}, nil
	case err != nil:
		return nil, err
	}
	current, _, err := install.LinkTarget(link, info)
	if err != nil {
		return nil, err
	}
	same := current != "" && install.SameTarget(link, current, target)
	switch {
	case same && !ours, same && key == link && owned == entry:
		return nil, nil
	case same:
		return &engine.Change{
			Action: engine.Update, Target: label, Detail: "track " + detail,
			Apply: func(context.Context) error { return m.updateSkills(record) },
		}, nil
	case ours && current != "" && install.SameTarget(link, current, owned.Target):
		if err := needLinks(); err != nil {
			return nil, err
		}
		return &engine.Change{
			Action: engine.Update, Target: label, Detail: detail + " (was " + m.Paths.Pretty(owned.Target) + ")",
			Apply: func(ctx context.Context) error {
				if err := os.Remove(link); err != nil {
					return err
				}
				return create(ctx)
			},
		}, nil
	}
	return nil, fmt.Errorf("%s already exists and zakwas did not link it; move it away to link skill %q", m.Paths.Pretty(link), w.name)
}

// planSkillRemove removes a link zakwas made that no skill wants anymore;
// one that was changed since, or sits under a symlinked parent now, is only
// forgotten.
func (m *Module) planSkillRemove(link string, l skillLink) engine.Change {
	forget := func(st *skillsState) { delete(st.Links, link) }
	label := m.Paths.Pretty(link)
	current, err := os.Readlink(link)
	if err != nil || !install.SameTarget(link, current, l.Target) || install.CheckParents(m.Paths, link) != nil {
		return engine.Change{
			Action: engine.Remove, Target: label, Detail: "no longer zakwas's link, forget it",
			Apply: func(context.Context) error { return m.updateSkills(forget) },
		}
	}
	return engine.Change{
		Action: engine.Remove, Target: label, Detail: "skill " + l.Skill + " no longer declared", Destructive: true,
		Apply: func(context.Context) error {
			if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			return m.updateSkills(forget)
		},
	}
}

// planSourceRemove removes a clone zakwas made that no skill uses anymore,
// unless a link zakwas made still points into it, as after a failed clone
// of the source the skill moved to.
// A name in the state that isn't a plain directory name is only forgotten.
func (m *Module) planSourceRemove(id, src string) engine.Change {
	forget := func(st *skillsState) { delete(st.Sources, id) }
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return engine.Change{
			Action: engine.Remove, Target: "skills source " + src, Detail: "not a directory zakwas made, forget it",
			Apply: func(context.Context) error { return m.updateSkills(forget) },
		}
	}
	dir := filepath.Join(m.skillsCacheDir(), id)
	return engine.Change{
		Action: engine.Remove, Target: "skills source " + src, Detail: m.Paths.Pretty(dir), Destructive: true,
		Apply: func(context.Context) error {
			st, err := m.loadSkills()
			if err != nil {
				return err
			}
			for _, l := range st.Links {
				if within(filepath.Clean(l.Target), dir) {
					return nil
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			return m.updateSkills(forget)
		},
	}
}

// refreshSkills pulls the git sources of agents.skills that zakwas already
// cloned, for `zakwas upgrade`; apply clones missing ones.
func (m *Module) refreshSkills(ctx context.Context) error {
	var sources []skillSource
	seen := map[string]bool{}
	for _, s := range m.Agents.Skills {
		if src := newSkillSource(s); s.Git != "" && !seen[src.id] {
			seen[src.id] = true
			sources = append(sources, src)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	if err := m.checkSkillsOwnDirs(); err != nil {
		return err
	}
	st, err := m.loadSkills()
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range sources {
		if !m.fetched(st, s) {
			continue
		}
		dir := filepath.Join(m.skillsCacheDir(), s.id)
		if err := gitPull(ctx, m.Runner, dir, s.src); err != nil {
			errs = append(errs, fmt.Errorf("skills source %s: %w; to fetch it fresh, remove %s and apply again", s.src, err, m.Paths.Pretty(dir)))
		}
	}
	return errors.Join(errs...)
}
