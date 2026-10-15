// Package launchd installs launchd user agents for scheduled jobs. zakwas
// writes each job's plist to ~/Library/LaunchAgents and loads it with
// launchctl; it unloads and deletes only jobs it created, tracked in
// ~/.local/state/zakwas/launchd.json.
package launchd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
)

// UnloadPoll and UnloadWait bound how long apply waits for launchd to finish
// unloading a job: bootstrapping it again while it is still going away fails.
var (
	UnloadPoll = 100 * time.Millisecond
	UnloadWait = 10 * time.Second
)

// StatePath is where zakwas records the jobs it created.
func StatePath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "launchd.json")
}

// Module converges the launchd user agents. UID selects the user's launchd
// domain, gui/<uid>.
type Module struct {
	Jobs   []config.LaunchdJob
	Paths  config.Paths
	Runner runner.Runner
	UID    int

	state *state
}

func (m *Module) Name() string { return "launchd" }

// state maps each label zakwas created to the hash of the plist it wrote.
type state struct {
	path string
	Jobs map[string]string `json:"jobs"`
}

func loadState(path string) (*state, error) {
	s := &state{path: path, Jobs: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.Jobs == nil {
		s.Jobs = map[string]string{}
	}
	for label := range s.Jobs {
		if !config.ValidLaunchdLabel(label) {
			return nil, fmt.Errorf("%s: invalid label %q", path, label)
		}
	}
	return s, nil
}

func (s *state) save() error {
	if len(s.Jobs) == 0 {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return install.WriteAtomic(s.path, append(data, '\n'), 0o644)
}

func (s *state) record(label, sum string) error {
	s.Jobs[label] = sum
	return s.save()
}

func (s *state) forget(label string) error {
	delete(s.Jobs, label)
	return s.save()
}

var errLoadedElsewhere = errors.New("already loaded from another plist; zakwas only manages jobs it created, so unload it or use another label")

func (m *Module) domain() string { return "gui/" + strconv.Itoa(m.UID) }

func (m *Module) plistPath(label string) string {
	return m.Paths.Dst(config.LaunchAgentsDir + "/" + label + ".plist")
}

func launchctl(args ...string) runner.Cmd {
	return runner.Cmd{Name: "launchctl", Args: args}
}

func (m *Module) loaded(ctx context.Context, label string) (bool, error) {
	res, err := m.Runner.Run(ctx, launchctl("print", m.domain()+"/"+label))
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

func (m *Module) bootstrap(ctx context.Context, label string) error {
	return runner.Check(ctx, m.Runner, launchctl("bootstrap", m.domain(), m.plistPath(label)))
}

func (m *Module) bootout(ctx context.Context, label string) error {
	if err := runner.Check(ctx, m.Runner, launchctl("bootout", m.domain()+"/"+label)); err != nil {
		return err
	}
	deadline := time.Now().Add(UnloadWait)
	for {
		on, err := m.loaded(ctx, label)
		if err != nil || !on {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still loaded %s after bootout", label, UnloadWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(UnloadPoll):
		}
	}
}

// unload boots the job out when it is loaded.
func (m *Module) unload(ctx context.Context, label string) error {
	on, err := m.loaded(ctx, label)
	if err != nil || !on {
		return err
	}
	return m.bootout(ctx, label)
}

func (m *Module) loadState() error {
	s, err := loadState(StatePath(m.Paths.Home))
	if err != nil {
		return err
	}
	m.state = s
	return nil
}

// Plan reads the state file, the plists and launchd's loaded jobs; it never
// loads or unloads anything. A job that can't be planned is reported next to
// the changes of the others.
func (m *Module) Plan(ctx context.Context) ([]engine.Change, error) {
	if err := m.loadState(); err != nil {
		return nil, err
	}
	if len(m.Jobs) == 0 && len(m.state.Jobs) == 0 {
		return nil, nil
	}
	if !m.Runner.Installed("launchctl") {
		return nil, errors.New("launchctl not found: launchd jobs need macOS")
	}
	var changes []engine.Change
	var errs []error
	wanted := map[string]bool{}
	for _, j := range m.Jobs {
		wanted[j.Label] = true
	}
	var gone []string
	for label := range m.state.Jobs {
		if !wanted[label] {
			gone = append(gone, label)
		}
	}
	slices.Sort(gone)
	var removals []engine.Change
	for _, label := range gone {
		var c *engine.Change
		var err error
		if m.renamed(label) {
			c, err = m.planRename(ctx, label)
		} else {
			c, err = m.planRemove(ctx, label)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			continue
		}
		removals = append(removals, *c)
	}
	for _, j := range m.Jobs {
		c, err := m.planJob(ctx, j)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", j.Label, err))
			continue
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	return append(removals, changes...), errors.Join(errs...)
}

// renamed reports whether a tracked label only differs in case from a
// configured one. Both name the same plist on APFS, which is case-insensitive,
// but launchd tells labels apart.
func (m *Module) renamed(label string) bool {
	return slices.ContainsFunc(m.Jobs, func(j config.LaunchdJob) bool {
		return j.Label != label && strings.EqualFold(j.Label, label)
	})
}

// trackedSum finds the hash recorded for label, under any spelling of its case.
func (m *Module) trackedSum(label string) (string, bool) {
	if sum, ok := m.state.Jobs[label]; ok {
		return sum, true
	}
	for k, sum := range m.state.Jobs {
		if strings.EqualFold(k, label) {
			return sum, true
		}
	}
	return "", false
}

// planRename unloads the job under its old label and drops it from the state;
// the plist stays, since the configured job rewrites it.
func (m *Module) planRename(ctx context.Context, label string) (*engine.Change, error) {
	on, err := m.loaded(ctx, label)
	if err != nil {
		return nil, err
	}
	detail := "label case changed, forget old label"
	if on {
		detail = "label case changed, unload old label"
	}
	return &engine.Change{
		Action: engine.Remove, Target: label, Detail: detail,
		Apply: func(ctx context.Context) error {
			if err := m.unload(ctx, label); err != nil {
				return err
			}
			return m.state.forget(label)
		},
	}, nil
}

func (m *Module) planJob(ctx context.Context, j config.LaunchdJob) (*engine.Change, error) {
	want, err := render(j, m.Paths)
	if err != nil {
		return nil, err
	}
	sum := install.Sum(want)
	path := m.plistPath(j.Label)
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	on, err := m.loaded(ctx, j.Label)
	if err != nil {
		return nil, err
	}
	recorded, tracked := m.trackedSum(j.Label)
	change := func(a engine.Action, detail string, apply func(context.Context) error) *engine.Change {
		return &engine.Change{Action: a, Target: j.Label, Detail: detail, Apply: apply}
	}

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if on && !tracked {
			return nil, errLoadedElsewhere
		}
		c := change(engine.Create, "write plist, load", m.writeAndLoad(j.Label, want, sum, false))
		c.Diff = udiff.Unified("/dev/null", "repo", "", string(want))
		return c, nil
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file; move it away first", path)
	}

	have, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(have, want) {
		switch {
		case !on:
			return change(engine.Update, "load", m.loadOnly(j.Label, sum)), nil
		case !tracked || recorded != sum:
			return change(engine.Update, "already matches, start tracking", m.track(j.Label, sum)), nil
		}
		return nil, nil
	}

	if on && !tracked {
		return nil, errLoadedElsewhere
	}
	edited := !tracked || recorded != install.Sum(have)
	detail := "plist changed, reload"
	if edited {
		why := "edited in place"
		if !tracked {
			why = "not managed yet"
		}
		detail = fmt.Sprintf("%s, back up to %s, reload", why, filepath.Base(install.BackupName(path)))
	}
	c := change(engine.Update, detail, m.writeAndLoad(j.Label, want, sum, edited))
	c.Diff = udiff.Unified(path, "repo", string(have), string(want))
	return c, nil
}

// writeAndLoad installs the plist and reloads the job. The old job is
// unloaded before anything is written, so a failed unload leaves the file and
// state as they were and the next plan reloads again. State is recorded once
// the file is in place, so a failed load is retried by the next plan.
func (m *Module) writeAndLoad(label string, want []byte, sum string, backup bool) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := m.unload(ctx, label); err != nil {
			return err
		}
		path := m.plistPath(label)
		if backup {
			if _, err := install.Backup(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := install.WriteAtomic(path, want, 0o644); err != nil {
			return err
		}
		if err := m.state.record(label, sum); err != nil {
			return err
		}
		return m.bootstrap(ctx, label)
	}
}

func (m *Module) loadOnly(label, sum string) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := m.state.record(label, sum); err != nil {
			return err
		}
		if on, err := m.loaded(ctx, label); err != nil || on {
			return err
		}
		return m.bootstrap(ctx, label)
	}
}

func (m *Module) track(label, sum string) func(context.Context) error {
	return func(context.Context) error { return m.state.record(label, sum) }
}

// planRemove retires a job zakwas created and the config no longer lists. A
// plist that was edited since is backed up instead of deleted, and anything
// that is no longer a regular file is left in place.
func (m *Module) planRemove(ctx context.Context, label string) (*engine.Change, error) {
	path := m.plistPath(label)
	if err := install.CheckParents(m.Paths, path); err != nil {
		return nil, err
	}
	on, err := m.loaded(ctx, label)
	if err != nil {
		return nil, err
	}
	detail := "plist already gone"
	destructive := on
	var file func() error

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case !info.Mode().IsRegular():
		detail = "plist is no longer a regular file, left in place"
	default:
		have, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if m.state.Jobs[label] == install.Sum(have) {
			detail, destructive = "delete plist", true
			file = func() error { return os.Remove(path) }
		} else {
			detail = "plist edited, back up to " + filepath.Base(install.BackupName(path))
			file = func() error { _, err := install.Backup(path); return err }
		}
	}
	if on {
		detail = "unload, " + detail
	}
	return &engine.Change{
		Action: engine.Remove, Target: label, Detail: detail, Destructive: destructive,
		Apply: func(ctx context.Context) error {
			if err := m.unload(ctx, label); err != nil {
				return err
			}
			if file != nil {
				if err := file(); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			return m.state.forget(label)
		},
	}, nil
}
