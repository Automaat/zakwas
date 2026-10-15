package launchd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const uid = 501

var (
	loadedOK  = runner.Result{}
	notLoaded = runner.Result{ExitCode: 113, Stderr: "Could not find service"}
)

func TestMain(m *testing.M) {
	UnloadPoll = time.Millisecond
	os.Exit(m.Run())
}

func job(label string) config.LaunchdJob {
	return config.LaunchdJob{
		Label:    label,
		Program:  []string{"~/.local/bin/zakwas-check-notify"},
		Schedule: config.LaunchdSchedule{At: "10:00"},
	}
}

func printCmd(label string) string { return "launchctl print gui/501/" + label }

type env struct {
	home string
	fake *runnertest.Fake
	mod  *Module
}

func newEnv(t *testing.T, jobs ...config.LaunchdJob) *env {
	t.Helper()
	home := t.TempDir()
	fake := runnertest.New()
	paths := config.Paths{Home: home, Root: t.TempDir()}
	return &env{home: home, fake: fake, mod: &Module{Jobs: jobs, Paths: paths, Runner: fake, UID: uid}}
}

func (e *env) plistPath(label string) string {
	return filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")
}

func (e *env) writePlist(t *testing.T, label string, data string) {
	t.Helper()
	p := e.plistPath(label)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *env) track(t *testing.T, label, data string) {
	t.Helper()
	s, err := loadState(StatePath(e.home))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.record(label, install.Sum([]byte(data))); err != nil {
		t.Fatal(err)
	}
}

func (e *env) trackedLabels(t *testing.T) []string {
	t.Helper()
	s, err := loadState(StatePath(e.home))
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for l := range s.Jobs {
		labels = append(labels, l)
	}
	slices.Sort(labels)
	return labels
}

func (e *env) rendered(t *testing.T, j config.LaunchdJob) string {
	t.Helper()
	data, err := render(j, e.mod.Paths)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (e *env) plan(t *testing.T) []engine.Change {
	t.Helper()
	changes, err := e.mod.Plan(context.Background())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return changes
}

func (e *env) apply(t *testing.T, changes []engine.Change) error {
	t.Helper()
	return enginetest.Apply(context.Background(), engine.Plan{{Module: "launchd", Changes: changes}})
}

func mutating(lines []string) []string {
	var out []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "launchctl print ") {
			out = append(out, l)
		}
	}
	return out
}

func TestPlanJob(t *testing.T) {
	j := job("com.example.check")
	tests := []struct {
		name    string
		plist   func(want string) string
		tracked func(want string) string
		loaded  bool
		action  engine.Action
		detail  string
		wantErr string
	}{
		{name: "new job", plist: none, tracked: none, action: engine.Create, detail: "write plist, load"},
		{name: "new job, plist deleted by hand", plist: none, tracked: same, loaded: true, action: engine.Create, detail: "write plist, load"},
		{name: "converged", plist: same, tracked: same, loaded: true},
		{name: "plist matches but not loaded", plist: same, tracked: same, action: engine.Update, detail: "load"},
		{name: "plist matches, never tracked", plist: same, tracked: none, loaded: true, action: engine.Update, detail: "already matches, start tracking"},
		{name: "job changed in config", plist: stale, tracked: staleSum, loaded: true, action: engine.Update, detail: "plist changed, reload"},
		{name: "plist edited in place", plist: edited, tracked: staleSum, loaded: true, action: engine.Update, detail: "edited in place, back up to com.example.check.plist.zakwas-bak, reload"},
		{name: "foreign plist at the path", plist: stale, tracked: none, action: engine.Update, detail: "not managed yet, back up to com.example.check.plist.zakwas-bak, reload"},
		{name: "label loaded from another plist", plist: none, tracked: none, loaded: true, wantErr: "already loaded from another plist"},
		{name: "loaded label with a foreign plist at the path", plist: stale, tracked: none, loaded: true, wantErr: "already loaded from another plist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, j)
			want := e.rendered(t, j)
			if p := tt.plist(want); p != "" {
				e.writePlist(t, j.Label, p)
			}
			if s := tt.tracked(want); s != "" {
				e.track(t, j.Label, s)
			}
			if tt.loaded {
				e.fake.On(printCmd(j.Label), loadedOK)
			} else {
				e.fake.On(printCmd(j.Label), notLoaded)
			}

			changes, err := e.mod.Plan(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.action == "" {
				if len(changes) != 0 {
					t.Fatalf("changes = %v, want none", changes)
				}
			} else if len(changes) != 1 || changes[0].Action != tt.action || changes[0].Detail != tt.detail {
				t.Fatalf("changes = %v, want %s (%s)", changes, tt.action, tt.detail)
			}
			if got := e.fake.Lines(); len(mutating(got)) != 0 {
				t.Errorf("plan changed launchd state: %v", got)
			}
		})
	}
}

func none(string) string   { return "" }
func same(w string) string { return w }
func stale(string) string  { return "<plist>old</plist>\n" }
func edited(string) string { return "<plist>hand edited</plist>\n" }
func staleSum(string) string {
	return stale("")
}

func TestApplyConvergesAndSecondPlanIsEmpty(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.fake.
		On(printCmd(j.Label), notLoaded).
		On(printCmd(j.Label), notLoaded).
		On(printCmd(j.Label), loadedOK).
		On("launchctl bootstrap gui/501 "+e.plistPath(j.Label), loadedOK)

	changes := e.plan(t)
	if len(changes) != 1 || changes[0].Action != engine.Create {
		t.Fatalf("first plan = %v", changes)
	}
	if err := e.apply(t, changes); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(e.plistPath(j.Label))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != e.rendered(t, j) {
		t.Errorf("plist = %s", got)
	}
	if want := []string{"launchctl bootstrap gui/501 " + e.plistPath(j.Label)}; !slices.Equal(mutating(e.fake.Lines()), want) {
		t.Errorf("mutating calls = %v, want %v", mutating(e.fake.Lines()), want)
	}
	if labels := e.trackedLabels(t); !slices.Equal(labels, []string{j.Label}) {
		t.Errorf("tracked = %v", labels)
	}
	if again := e.plan(t); len(again) != 0 {
		t.Errorf("second plan = %v, want empty", again)
	}
}

func TestChangedJobIsReloaded(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.writePlist(t, j.Label, "<plist>old</plist>\n")
	e.track(t, j.Label, "<plist>old</plist>\n")
	e.fake.
		On(printCmd(j.Label), loadedOK).
		On(printCmd(j.Label), loadedOK).
		On(printCmd(j.Label), notLoaded).
		On("launchctl bootout gui/501/"+j.Label, loadedOK).
		On("launchctl bootstrap gui/501 "+e.plistPath(j.Label), loadedOK)

	changes := e.plan(t)
	if err := e.apply(t, changes); err != nil {
		t.Fatal(err)
	}
	want := []string{"launchctl bootout gui/501/" + j.Label, "launchctl bootstrap gui/501 " + e.plistPath(j.Label)}
	if got := mutating(e.fake.Lines()); !slices.Equal(got, want) {
		t.Errorf("mutating calls = %v, want %v", got, want)
	}
	if got, _ := os.ReadFile(e.plistPath(j.Label)); string(got) != e.rendered(t, j) {
		t.Errorf("plist not updated: %s", got)
	}
	if _, err := os.Stat(e.plistPath(j.Label) + ".zakwas-bak"); !errors.Is(err, os.ErrNotExist) {
		t.Error("an unedited plist must not leave a backup")
	}
}

func TestEditedPlistIsBackedUpBeforeReplacing(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.writePlist(t, j.Label, "<plist>hand edited</plist>\n")
	e.fake.On(printCmd(j.Label), notLoaded).On("launchctl bootstrap gui/501 "+e.plistPath(j.Label), loadedOK)

	if err := e.apply(t, e.plan(t)); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(e.plistPath(j.Label) + ".zakwas-bak")
	if err != nil || string(bak) != "<plist>hand edited</plist>\n" {
		t.Errorf("backup = %q, %v", bak, err)
	}
}

func TestRemovedJobIsUnloadedAndDeleted(t *testing.T) {
	tests := []struct {
		name       string
		content    func(rendered string) string
		wantBackup bool
	}{
		{name: "untouched plist is deleted", content: same},
		{name: "edited plist is backed up", content: edited, wantBackup: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := job("com.example.gone")
			e := newEnv(t)
			original := e.rendered(t, j)
			e.writePlist(t, j.Label, tt.content(original))
			e.track(t, j.Label, original)
			e.fake.
				On(printCmd(j.Label), loadedOK).
				On(printCmd(j.Label), loadedOK).
				On(printCmd(j.Label), notLoaded).
				On("launchctl bootout gui/501/"+j.Label, loadedOK)

			changes := e.plan(t)
			if len(changes) != 1 || changes[0].Action != engine.Remove || !changes[0].Destructive {
				t.Fatalf("plan = %v", changes)
			}
			if err := e.apply(t, changes); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(mutating(e.fake.Lines()), []string{"launchctl bootout gui/501/" + j.Label}) {
				t.Errorf("calls = %v", e.fake.Lines())
			}
			if _, err := os.Lstat(e.plistPath(j.Label)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("plist still there: %v", err)
			}
			_, err := os.Stat(e.plistPath(j.Label) + ".zakwas-bak")
			if tt.wantBackup != (err == nil) {
				t.Errorf("backup exists = %v, want %v", err == nil, tt.wantBackup)
			}
			if labels := e.trackedLabels(t); len(labels) != 0 {
				t.Errorf("still tracked: %v", labels)
			}
			if _, err := os.Stat(StatePath(e.home)); !errors.Is(err, os.ErrNotExist) {
				t.Error("empty state file must be removed")
			}
			if again := e.plan(t); len(again) != 0 {
				t.Errorf("second plan = %v, want empty", again)
			}
		})
	}
}

func TestLabelsZakwasDidNotCreateAreNeverTouched(t *testing.T) {
	mine := job("com.example.mine")
	e := newEnv(t)
	body := e.rendered(t, mine)
	e.writePlist(t, mine.Label, body)
	e.track(t, mine.Label, body)
	e.writePlist(t, "com.apple.foreign", "<plist>theirs</plist>\n")
	e.fake.On(printCmd(mine.Label), notLoaded)

	changes := e.plan(t)
	if err := e.apply(t, changes); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(e.plistPath("com.apple.foreign")); err != nil || string(got) != "<plist>theirs</plist>\n" {
		t.Errorf("foreign plist changed: %q, %v", got, err)
	}
	for _, l := range e.fake.Lines() {
		if strings.Contains(l, "foreign") {
			t.Errorf("launchctl was asked about a foreign label: %s", l)
		}
	}
}

func TestNothingConfiguredNothingTrackedDoesNotNeedLaunchctl(t *testing.T) {
	e := newEnv(t)
	e.fake.Missing("launchctl")
	if changes := e.plan(t); len(changes) != 0 || len(e.fake.Lines()) != 0 {
		t.Errorf("changes = %v, calls = %v", changes, e.fake.Lines())
	}
}

func TestMissingLaunchctlFailsThePlan(t *testing.T) {
	e := newEnv(t, job("com.example.check"))
	e.fake.Missing("launchctl")
	if _, err := e.mod.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "launchctl not found") {
		t.Errorf("err = %v", err)
	}
}

func TestOneBadJobDoesNotBlockTheOthers(t *testing.T) {
	good, foreign := job("com.example.good"), job("com.example.foreign")
	e := newEnv(t, foreign, good)
	e.fake.On(printCmd(foreign.Label), loadedOK).On(printCmd(good.Label), notLoaded)

	changes, err := e.mod.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "com.example.foreign") {
		t.Errorf("err = %v", err)
	}
	if len(changes) != 1 || changes[0].Target != good.Label {
		t.Errorf("changes = %v", changes)
	}
}

func TestFailedLoadIsRetriedByTheNextPlan(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.fake.
		On(printCmd(j.Label), notLoaded).
		On("launchctl bootstrap gui/501 "+e.plistPath(j.Label), runner.Result{ExitCode: 5, Stderr: "Bootstrap failed: 5: Input/output error"})

	if err := e.apply(t, e.plan(t)); err == nil || !strings.Contains(err.Error(), "Input/output error") {
		t.Fatalf("apply err = %v", err)
	}
	again := e.plan(t)
	if len(again) != 1 || again[0].Detail != "load" {
		t.Errorf("next plan = %v, want a load", again)
	}
}

func TestStillLoadedAfterBootoutFails(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.writePlist(t, j.Label, "<plist>old</plist>\n")
	e.track(t, j.Label, "<plist>old</plist>\n")
	e.fake.
		On(printCmd(j.Label), loadedOK).
		On("launchctl bootout gui/501/"+j.Label, loadedOK)
	changes := e.plan(t)

	UnloadWait = 5 * time.Millisecond
	t.Cleanup(func() { UnloadWait = 10 * time.Second })
	if err := e.apply(t, changes); err == nil || !strings.Contains(err.Error(), "still loaded") {
		t.Errorf("err = %v", err)
	}
	if e.fake.Ran("launchctl bootstrap") {
		t.Error("must not bootstrap while the old job is still loaded")
	}
}

func TestCorruptStateFailsThePlan(t *testing.T) {
	e := newEnv(t, job("com.example.check"))
	if err := os.MkdirAll(filepath.Dir(StatePath(e.home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath(e.home), []byte(`{"jobs":{"../../escape":"x"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mod.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid label") {
		t.Errorf("err = %v", err)
	}
}

func TestSymlinkedLaunchAgentsDirIsRefused(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(e.home, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(e.home, "Library", "LaunchAgents")); err != nil {
		t.Fatal(err)
	}
	e.fake.On(printCmd(j.Label), notLoaded)
	if _, err := e.mod.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v", err)
	}
}

func TestFailedUnloadLeavesFileAndStateForTheNextPlan(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	e.writePlist(t, j.Label, "<plist>old</plist>\n")
	e.track(t, j.Label, "<plist>old</plist>\n")
	e.fake.
		On(printCmd(j.Label), loadedOK).
		On("launchctl bootout gui/501/"+j.Label, runner.Result{ExitCode: 5, Stderr: "Boot-out failed: 5: Input/output error"})

	if err := e.apply(t, e.plan(t)); err == nil || !strings.Contains(err.Error(), "Boot-out failed") {
		t.Fatalf("apply err = %v", err)
	}
	if got, _ := os.ReadFile(e.plistPath(j.Label)); string(got) != "<plist>old</plist>\n" {
		t.Errorf("plist was rewritten while the old job is still loaded: %s", got)
	}
	again := e.plan(t)
	if len(again) != 1 || again[0].Detail != "plist changed, reload" {
		t.Errorf("next plan = %v, want a reload", again)
	}
}

func TestLabelCaseChangeKeepsThePlist(t *testing.T) {
	j := job("com.example.check")
	e := newEnv(t, j)
	old := "<plist>old</plist>\n"
	e.writePlist(t, "Com.Example.Check", old)
	e.track(t, "Com.Example.Check", old)
	caseInsensitive := func() bool {
		_, err := os.Stat(e.plistPath(j.Label))
		return err == nil
	}()
	if !caseInsensitive {
		t.Skip("needs a case-insensitive file system")
	}
	e.fake.
		On(printCmd("Com.Example.Check"), loadedOK).
		On(printCmd("Com.Example.Check"), loadedOK).
		On(printCmd("Com.Example.Check"), notLoaded).
		On("launchctl bootout gui/501/Com.Example.Check", loadedOK).
		On(printCmd(j.Label), notLoaded).
		On(printCmd(j.Label), notLoaded).
		On(printCmd(j.Label), loadedOK).
		On("launchctl bootstrap gui/501 "+e.plistPath(j.Label), loadedOK)

	changes := e.plan(t)
	if len(changes) != 2 || changes[0].Target != "Com.Example.Check" || changes[1].Target != j.Label {
		t.Fatalf("plan = %v, want the old label retired before the job is written", changes)
	}
	if err := e.apply(t, changes); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(e.plistPath(j.Label)); err != nil || string(got) != e.rendered(t, j) {
		t.Errorf("plist = %q, %v", got, err)
	}
	if labels := e.trackedLabels(t); !slices.Equal(labels, []string{j.Label}) {
		t.Errorf("tracked = %v", labels)
	}
	if again := e.plan(t); len(again) != 0 {
		t.Errorf("second plan = %v, want empty", again)
	}
}

func TestPrintFailureIsNotTreatedAsNotLoaded(t *testing.T) {
	broken := runner.Result{ExitCode: 5, Stderr: "Print failed: 5: Input/output error"}
	t.Run("removal keeps plist and state", func(t *testing.T) {
		j := job("com.example.gone")
		e := newEnv(t)
		body := e.rendered(t, j)
		e.writePlist(t, j.Label, body)
		e.track(t, j.Label, body)
		e.fake.On(printCmd(j.Label), broken)

		changes, err := e.mod.Plan(context.Background())
		if err == nil || !strings.Contains(err.Error(), "Input/output error") || len(changes) != 0 {
			t.Fatalf("changes = %v, err = %v, want a plan error and no change", changes, err)
		}
		if _, err := os.Stat(e.plistPath(j.Label)); err != nil {
			t.Errorf("plist removed: %v", err)
		}
		if labels := e.trackedLabels(t); !slices.Equal(labels, []string{j.Label}) {
			t.Errorf("tracked = %v", labels)
		}
	})
	t.Run("apply stops before touching the file", func(t *testing.T) {
		j := job("com.example.check")
		e := newEnv(t, j)
		e.writePlist(t, j.Label, "<plist>old</plist>\n")
		e.track(t, j.Label, "<plist>old</plist>\n")
		e.fake.On(printCmd(j.Label), loadedOK)
		changes := e.plan(t)
		e.fake.On(printCmd(j.Label), broken)
		if err := e.apply(t, changes); err == nil {
			t.Fatal("apply succeeded although launchd state is unknown")
		}
		if got, _ := os.ReadFile(e.plistPath(j.Label)); string(got) != "<plist>old</plist>\n" {
			t.Errorf("plist rewritten: %s", got)
		}
	})
}
