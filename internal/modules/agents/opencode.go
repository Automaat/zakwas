package agents

import (
	"context"
	"encoding/json"
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
	"github.com/Automaat/zakwas/internal/runner"
)

// opencode has no plugin system: zakwas fetches git marketplaces into
// cacheDir, uses local ones in place, and links each skill of a declared
// plugin into skillsDir. statePath records what it fetched and linked, so
// prune only removes its own links.
type opencode struct {
	runner    runner.Runner
	paths     config.Paths
	skillsDir string
	cacheDir  string
	statePath string
}

func (m *Module) opencodeBackend() *opencode {
	return &opencode{
		runner:    m.Runner,
		paths:     m.Paths,
		skillsDir: m.Paths.Dst(m.Agents.OpencodeSkillsDir()),
		cacheDir:  filepath.Join(m.Paths.Home, ".local", "share", "zakwas", "agents"),
		statePath: filepath.Join(m.Paths.Home, ".local", "state", "zakwas", "opencode.json"),
	}
}

// opencodeState maps each fetched git marketplace to its source, each
// marketplace whose best-effort fetch failed to that source (retried by
// `zakwas upgrade`), and each skill link zakwas made (by absolute path, so
// a changed skillsDir still finds the old ones) to its target and plugin.
type opencodeState struct {
	Marketplaces map[string]string        `json:"marketplaces"`
	Failed       map[string]string        `json:"failed,omitempty"`
	Skills       map[string]opencodeSkill `json:"skills"`
}

type opencodeSkill struct {
	Target string `json:"target"`
	Plugin string `json:"plugin"`
}

func (o *opencode) load() (opencodeState, error) {
	st := opencodeState{Marketplaces: map[string]string{}, Failed: map[string]string{}, Skills: map[string]opencodeSkill{}}
	data, err := os.ReadFile(o.statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", o.statePath, err)
	}
	if st.Marketplaces == nil {
		st.Marketplaces = map[string]string{}
	}
	if st.Skills == nil {
		st.Skills = map[string]opencodeSkill{}
	}
	if st.Failed == nil {
		st.Failed = map[string]string{}
	}
	return st, nil
}

// update rewrites the state after each step, so an interrupted apply keeps
// track of every link it made.
func (o *opencode) update(edit func(*opencodeState)) error {
	st, err := o.load()
	if err != nil {
		return err
	}
	edit(&st)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(o.statePath), ".opencode.json.zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), o.statePath)
}

func (o *opencode) git(args ...string) runner.Cmd {
	return runner.Cmd{Name: "git", Args: args, Dir: "/", Env: []string{"GIT_TERMINAL_PROMPT=0"}}
}

// gitSource splits a git marketplace source into a clone URL and ref.
func gitSource(src string) (url, ref string) {
	url, ref, _ = strings.Cut(src, "#")
	if githubShort.MatchString(url) {
		url = "https://github.com/" + strings.TrimSuffix(url, ".git") + ".git"
	}
	return url, ref
}

// root is where a marketplace's files are: a local one in place, a git one
// in the cache.
func (o *opencode) root(name, src string) string {
	if IsLocalSource(src) {
		return src
	}
	return filepath.Join(o.cacheDir, name)
}

func (o *opencode) fetched(st opencodeState, name, src string) bool {
	have, ok := st.Marketplaces[name]
	if !ok || canonical(have) != canonical(src) {
		return false
	}
	info, err := os.Stat(filepath.Join(o.root(name, src), ".git"))
	return err == nil && info.IsDir()
}

// plan fetches nothing: a git marketplace not fetched yet plans a fetch.
// Its plugins' skills are only known after it, so the link and prune
// changes are then only listed, and a last step applies them once the
// fetch is done, checking every skill name before linking any.
//
// Plugins that target opencode only by the all-providers default are best
// effort: whatever keeps one from being linked skips it rather than
// failing the agents plan, so a Claude-only config keeps converging once
// opencode is installed.
func (o *opencode) plan(ctx context.Context, d desired) ([]engine.Change, error) {
	return o.planFrom(d, true)
}

// planFrom plans with or without fetching missing marketplaces; converge
// plans without, so a fetch that failed for a default-targeted plugin is
// not retried in a loop.
func (o *opencode) planFrom(d desired, fetch bool) ([]engine.Change, error) {
	if !o.runner.Installed("opencode") {
		if !slices.Contains(slices.Collect(maps.Values(d.explicit)), true) {
			return nil, nil
		}
		return nil, errors.New("opencode: opencode is not installed (no opencode on PATH); install it, or remove opencode from agents.providers")
	}
	d = usedMarketplaces(d)
	if err := o.checkOwnDirs(d); err != nil {
		return nil, err
	}
	st, err := o.load()
	if err != nil {
		return nil, err
	}
	var fetches []engine.Change
	pending := map[string]bool{}
	if fetch {
		if fetches, pending, err = o.planFetches(d, st); err != nil {
			return nil, err
		}
	}
	want, waiting, err := o.wantedSkills(d, st, pending)
	if err != nil {
		return nil, err
	}
	var changes []engine.Change
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(want)) {
		c, err := o.planLink(name, want[name].opencodeSkill, st)
		if err != nil && !want[name].strict {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if d.prune {
		changes = append(changes, o.planPrune(d, st, want, waiting)...)
	}
	if len(waiting) == 0 {
		return append(fetches, changes...), nil
	}
	shown := map[string]bool{}
	for i := range changes {
		changes[i].Apply = nil
		if changes[i].Destructive {
			shown[changes[i].Target] = true
		}
	}
	ids := slices.Sorted(maps.Keys(waiting))
	return append(append(fetches, changes...), engine.Change{
		Action: engine.Create, Target: "opencode skills", Detail: "of " + strings.Join(ids, ", ") + " once fetched",
		Apply: func(ctx context.Context) error { return o.converge(ctx, d, shown, waiting) },
	}), nil
}

// checkOwnDirs refuses zakwas's state and cache paths when reached through
// a symlinked directory under $HOME, which could point into the config
// repo.
func (o *opencode) checkOwnDirs(d desired) error {
	paths := []string{o.statePath}
	for name, src := range d.sources {
		if !IsLocalSource(src) {
			paths = append(paths, filepath.Join(o.cacheDir, name))
		}
	}
	var errs []error
	for _, p := range paths {
		if err := install.CheckParents(o.paths, p); err != nil {
			errs = append(errs, fmt.Errorf("opencode: %w", err))
		}
	}
	return errors.Join(errs...)
}

// planFetches plans a fetch of every declared git marketplace that isn't in
// the cache from its declared source, and returns those as pending.
func (o *opencode) planFetches(d desired, st opencodeState) ([]engine.Change, map[string]bool, error) {
	var changes []engine.Change
	var errs []error
	pending := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(d.sources)) {
		src := d.sources[name]
		strict := explicitIn(d, name)
		if IsLocalSource(src) || o.fetched(st, name, src) {
			continue
		}
		if failed, ok := st.Failed[name]; ok && !strict && canonical(failed) == canonical(src) {
			continue
		}
		if !o.runner.Installed("git") {
			if strict {
				errs = append(errs, fmt.Errorf("opencode: marketplace %q needs git to fetch %s, and git is not installed", name, src))
			}
			continue
		}
		pending[name] = true
		c := engine.Change{
			Action: engine.Create, Target: "opencode marketplace " + name, Detail: "fetch " + src,
			Apply: func(ctx context.Context) error {
				err := o.fetch(ctx, name, src)
				if err == nil || strict || ctx.Err() != nil {
					return err
				}
				return o.update(func(st *opencodeState) { st.Failed[name] = src })
			},
		}
		if have, ok := st.Marketplaces[name]; ok && canonical(have) != canonical(src) {
			c.Action, c.Detail = engine.Update, "fetch "+src+" (was "+have+")"
		}
		changes = append(changes, c)
	}
	return changes, pending, errors.Join(errs...)
}

// usedMarketplaces narrows d to the marketplaces of its plugins: opencode
// only needs their files, so others are neither fetched nor pulled.
func usedMarketplaces(d desired) desired {
	used := map[string]string{}
	for _, id := range d.plugins {
		if _, mk, ok := config.SplitPluginID(id); ok {
			if src, declared := d.sources[mk]; declared {
				used[mk] = src
			}
		}
	}
	d.sources = used
	return d
}

// explicitIn reports whether a plugin of marketplace mk targets opencode
// by name.
func explicitIn(d desired, mk string) bool {
	for _, id := range d.plugins {
		if _, m, _ := config.SplitPluginID(id); m == mk && d.explicit[id] {
			return true
		}
	}
	return false
}

// wantedSkill is a skill to link; strict ones come from plugins that target
// opencode by name, and fail the plan when they can't be linked.
type wantedSkill struct {
	opencodeSkill
	strict bool
}

type marketManifests struct {
	ms  manifests
	err error
}

// wantedSkills maps each skill of the declared plugins to its link target;
// plugins of pending marketplaces are returned in waiting instead. Two
// plugins shipping one skill name always fail, as a config conflict only
// the user can settle. Skill names are compared case-insensitively, as
// macOS's file system does.
func (o *opencode) wantedSkills(d desired, st opencodeState, pending map[string]bool) (map[string]wantedSkill, map[string]bool, error) {
	want := map[string]wantedSkill{}
	folded := map[string]string{}
	waiting := map[string]bool{}
	byMarket := map[string]marketManifests{}
	var errs []error
	for _, id := range d.plugins {
		plugin, mk, _ := config.SplitPluginID(id)
		src, ok := d.sources[mk]
		if !ok {
			continue
		}
		if pending[mk] {
			waiting[id] = true
			continue
		}
		strict := d.explicit[id]
		fail := func(err error) {
			if strict {
				errs = append(errs, err)
			}
		}
		if !IsLocalSource(src) && !o.fetched(st, mk, src) {
			fail(fmt.Errorf("opencode: marketplace %q is not fetched from %s", mk, src))
			continue
		}
		root := o.root(mk, src)
		mm, seen := byMarket[mk]
		if !seen {
			mm.ms, mm.err = readManifests(root)
			if name := mm.ms.otherName(mk); mm.err == nil && name != "" {
				mm.err = fmt.Errorf("the marketplace at %s is named %q, not %q; use %q as its key in agents.marketplaces", src, name, mk, name)
			}
			byMarket[mk] = mm
		}
		if mm.err != nil {
			fail(fmt.Errorf("opencode: marketplace %q: %w", mk, mm.err))
			continue
		}
		dir, extra, err := mm.ms.pluginPath(root, plugin)
		if err != nil {
			fail(fmt.Errorf("opencode: %w", err))
			continue
		}
		skills, err := pluginSkills(root, dir, extra)
		if err != nil {
			fail(fmt.Errorf("opencode: plugin %s: %w", id, err))
			continue
		}
		for _, s := range skills {
			key := strings.ToLower(s.name)
			if other, dup := folded[key]; dup {
				errs = append(errs, fmt.Errorf("opencode: skill %q is shipped by both %s and %s; opencode loads one skill per name, so target one of them at other providers", s.name, want[other].Plugin, id))
				continue
			}
			folded[key] = s.name
			want[s.name] = wantedSkill{opencodeSkill: opencodeSkill{Target: s.path, Plugin: id}, strict: strict}
		}
	}
	return want, waiting, errors.Join(errs...)
}

func (o *opencode) linkPath(name string) string { return filepath.Join(o.skillsDir, name) }

// label names a link in the plan: by skill name in skillsDir, by path
// elsewhere.
func (o *opencode) label(link string) string {
	if filepath.Dir(link) == o.skillsDir {
		return "opencode skill " + filepath.Base(link)
	}
	return "opencode skill " + o.paths.Pretty(link)
}

// owner finds the state entry of link, matching case-insensitively like
// macOS's file system, so a skill renamed only in case is still zakwas's.
func (st opencodeState) owner(link string) (string, opencodeSkill, bool) {
	if s, ok := st.Skills[link]; ok {
		return link, s, true
	}
	for key, s := range st.Skills {
		if strings.EqualFold(key, link) {
			return key, s, true
		}
	}
	return "", opencodeSkill{}, false
}

// planLink links one skill. It replaces only links zakwas made; a link the
// user made to the same skill is left alone and untracked, and anything
// else at the path fails the plan.
func (o *opencode) planLink(name string, s opencodeSkill, st opencodeState) (*engine.Change, error) {
	link := o.linkPath(name)
	if err := install.CheckParents(o.paths, link); err != nil {
		return nil, fmt.Errorf("opencode: %w, or set agents.opencode.skillsDir to a directory zakwas may write to", err)
	}
	target := o.label(link)
	key, owned, ours := st.owner(link)
	record := func(st *opencodeState) {
		if ours {
			delete(st.Skills, key)
		}
		st.Skills[link] = s
	}
	create := func(context.Context) error {
		if err := os.MkdirAll(o.skillsDir, 0o755); err != nil {
			return err
		}
		if err := os.Symlink(s.Target, link); err != nil {
			return err
		}
		return o.update(record)
	}
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &engine.Change{Action: engine.Create, Target: target, Detail: s.Plugin, Apply: create}, nil
	case err != nil:
		return nil, err
	}
	current := ""
	if info.Mode()&fs.ModeSymlink != 0 {
		if current, err = os.Readlink(link); err != nil {
			return nil, err
		}
	}
	switch {
	case current == s.Target && (!ours || key == link && owned == s):
		return nil, nil
	case current == s.Target:
		return &engine.Change{
			Action: engine.Update, Target: target, Detail: "track as " + s.Plugin,
			Apply: func(context.Context) error { return o.update(record) },
		}, nil
	case ours && current != "" && current == owned.Target:
		return &engine.Change{
			Action: engine.Update, Target: target, Detail: s.Plugin + " (was " + owned.Plugin + ")",
			Apply: func(ctx context.Context) error {
				if err := os.Remove(link); err != nil {
					return err
				}
				return create(ctx)
			},
		}, nil
	}
	return nil, fmt.Errorf("opencode: %s already exists and zakwas did not install it; move it away to install skill %q of %s", o.paths.Pretty(link), name, s.Plugin)
}

// planPrune removes the skill links zakwas made that no declared plugin
// ships anymore, in this skillsDir or an earlier one, then the fetched
// marketplaces no opencode plugin uses. Skills of plugins waiting for a
// fetch are kept: apply links them again. A link that was changed since is
// only forgotten.
func (o *opencode) planPrune(d desired, st opencodeState, want map[string]wantedSkill, waiting map[string]bool) []engine.Change {
	var changes []engine.Change
	wanted := map[string]bool{}
	for name := range want {
		wanted[strings.ToLower(o.linkPath(name))] = true
	}
	for _, link := range slices.Sorted(maps.Keys(st.Skills)) {
		s := st.Skills[link]
		if wanted[strings.ToLower(link)] || waiting[s.Plugin] {
			continue
		}
		forget := func(st *opencodeState) { delete(st.Skills, link) }
		current, err := os.Readlink(link)
		if err != nil || current != s.Target || install.CheckParents(o.paths, link) != nil {
			changes = append(changes, engine.Change{
				Action: engine.Remove, Target: o.label(link), Detail: "no longer zakwas's link, forget it",
				Apply: func(context.Context) error { return o.update(forget) },
			})
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: o.label(link), Detail: s.Plugin, Destructive: true,
			Apply: func(context.Context) error {
				if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				return o.update(forget)
			},
		})
	}
	for _, name := range slices.Sorted(maps.Keys(st.Marketplaces)) {
		dir := filepath.Join(o.cacheDir, name)
		if src, ok := d.sources[name]; ok && (!IsLocalSource(src) || within(canonical(src), canonical(dir))) {
			continue
		}
		changes = append(changes, engine.Change{
			Action: engine.Remove, Target: "opencode marketplace " + name, Detail: o.paths.Pretty(dir), Destructive: true,
			Apply: func(context.Context) error {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
				return o.update(func(st *opencodeState) { delete(st.Marketplaces, name) })
			},
		})
	}
	return changes
}

// converge plans again once the pending marketplaces are fetched and
// applies the result. A removal the plan didn't show is left for the next
// plan to show, unless it drops zakwas's own link to a skill of a plugin
// the plan listed as waiting for the fetch, or to one the fetch took away.
func (o *opencode) converge(ctx context.Context, d desired, shown, waiting map[string]bool) error {
	changes, err := o.planFrom(d, false)
	if err != nil {
		return err
	}
	st, err := o.load()
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for link, s := range st.Skills {
		if _, err := os.Stat(s.Target); errors.Is(err, fs.ErrNotExist) || waiting[s.Plugin] {
			allowed[o.label(link)] = true
		}
	}
	for _, c := range changes {
		if c.Destructive && !shown[c.Target] && !allowed[c.Target] {
			continue
		}
		if err := c.Apply(ctx); err != nil {
			return fmt.Errorf("%s: %w", c.Target, err)
		}
	}
	return nil
}

// fetch clones a marketplace next to its cache dir, then swaps it in, so a
// failed clone leaves the previous copy alone.
func (o *opencode) fetch(ctx context.Context, name, src string) error {
	if err := os.MkdirAll(o.cacheDir, 0o755); err != nil {
		return err
	}
	stale, _ := filepath.Glob(filepath.Join(o.cacheDir, "."+name+".zakwas-*"))
	for _, dir := range stale {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp(o.cacheDir, "."+name+".zakwas-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	url, ref := gitSource(src)
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	if err := runner.Check(ctx, o.runner, o.git(append(args, "--", url, tmp)...)); err != nil {
		return err
	}
	dir := filepath.Join(o.cacheDir, name)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return o.update(func(st *opencodeState) {
		st.Marketplaces[name] = src
		delete(st.Failed, name)
	})
}

// refresh pulls the declared git marketplaces zakwas already fetched;
// apply fetches missing ones fresh. A failed pull of a marketplace only
// default-targeted plugins use keeps the copy it has. git is pinned to the cache's own .git,
// so it can never walk up into an enclosing repo such as a dotfiles $HOME.
func (o *opencode) refresh(ctx context.Context, d desired) error {
	if !o.runner.Installed("opencode") {
		return nil
	}
	d = usedMarketplaces(d)
	st, err := o.load()
	if err != nil {
		return err
	}
	if len(st.Failed) > 0 {
		if err := o.update(func(st *opencodeState) { clear(st.Failed) }); err != nil {
			return err
		}
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(d.sources)) {
		src := d.sources[name]
		if IsLocalSource(src) || !o.fetched(st, name, src) {
			continue
		}
		dir := filepath.Join(o.cacheDir, name)
		_, ref := gitSource(src)
		if ref == "" {
			ref = "HEAD"
		}
		repo := []string{"--git-dir=" + filepath.Join(dir, ".git"), "--work-tree=" + dir}
		err := runner.Check(ctx, o.runner, o.git(append(repo, "fetch", "--quiet", "--depth", "1", "origin", ref)...))
		if err == nil {
			err = runner.Check(ctx, o.runner, o.git(append(repo, "reset", "--quiet", "--hard", "FETCH_HEAD")...))
		}
		if err != nil && (explicitIn(d, name) || ctx.Err() != nil) {
			errs = append(errs, fmt.Errorf("%w; to fetch it fresh, remove %s and apply again", err, o.paths.Pretty(dir)))
		}
	}
	return errors.Join(errs...)
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}
