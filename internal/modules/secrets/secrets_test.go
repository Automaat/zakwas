package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/testutil"
)

const token = "SESSION-TOKEN-VALUE"

// scripted replaces a Fake response for one test, since Fake queues them.
type scripted struct {
	*runnertest.Fake
	over   map[string]runner.Result
	during map[string]func()
}

func (s *scripted) Run(ctx context.Context, c runner.Cmd) (runner.Result, error) {
	res, err := s.Fake.Run(ctx, c)
	if hook, ok := s.during[c.String()]; ok {
		hook()
	}
	if over, ok := s.over[c.String()]; ok {
		return over, nil
	}
	return res, err
}

type fixture struct {
	home         string
	fake         *runnertest.Fake
	over         map[string]runner.Result
	during       map[string]func()
	module       *Module
	one, two     *age.X25519Identity
	keysPath     string
	keysEntryDst string
}

func identity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setup(t *testing.T) *fixture {
	t.Helper()
	testutil.SkipOnWindows(t, "the module is not supported there and file modes don't apply")
	f := &fixture{home: t.TempDir(), fake: runnertest.New(), over: map[string]runner.Result{}, during: map[string]func(){}, one: identity(t), two: identity(t), keysEntryDst: "~/keys/keys.txt"}
	f.keysPath = filepath.Join(f.home, "keys", "keys.txt")
	f.fake.OnOK("bw status", `{"serverUrl":null,"status":"locked","userEmail":"me@example.com"}`)
	f.fake.OnOK("bw unlock --raw", token+"\n")
	f.fake.OnOK("bw sync", "")
	f.fake.OnOK("bw lock", "")
	f.fake.OnOK("bw get notes note one", "# created: today\n# public key: x\n"+f.one.String()+"\n")
	f.fake.OnOK("bw get notes note two", f.two.String())
	f.module = &Module{
		Secrets: config.Secrets{Age: []config.AgeFile{{Dst: f.keysEntryDst, Keys: []config.AgeKey{
			{Bitwarden: "note one", PublicKey: f.one.Recipient().String()},
			{Bitwarden: "note two", PublicKey: f.two.Recipient().String()},
		}}}},
		Paths: config.Paths{Home: f.home}, Runner: &scripted{Fake: f.fake, over: f.over, during: f.during}, Interactive: true,
	}
	return f
}

func (f *fixture) write(t *testing.T, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.keysPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.keysPath, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) read(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.keysPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) plan(t *testing.T) []engine.Change {
	t.Helper()
	changes, err := f.module.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func (f *fixture) apply(t *testing.T) error {
	t.Helper()
	for _, c := range f.plan(t) {
		if c.Apply == nil {
			continue
		}
		if err := c.Apply(context.Background()); err != nil {
			return err
		}
	}
	return nil
}

func (f *fixture) bwCalls() []string { return f.fake.Lines() }

func TestPlanNamesMissingKeysWithoutStartingBw(t *testing.T) {
	f := setup(t)
	changes := f.plan(t)
	if len(changes) != 2 {
		t.Fatalf("changes = %v", changes)
	}
	if changes[0].Action != engine.Create || !strings.Contains(changes[0].Detail, "note one, note two") {
		t.Errorf("listing = %+v", changes[0])
	}
	if changes[1].Action != engine.Run || !changes[1].Streams {
		t.Errorf("step = %+v", changes[1])
	}
	if calls := f.bwCalls(); len(calls) != 0 {
		t.Errorf("plan ran %v", calls)
	}
}

func TestApplyRestoresThenConverges(t *testing.T) {
	f := setup(t)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if got, want := f.read(t), f.one.String()+"\n"+f.two.String()+"\n"; got != want {
		t.Error("the file should hold exactly the two identity lines, without the note's comments")
	}
	info, err := os.Stat(f.keysPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(f.keysPath))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o", dir.Mode().Perm())
	}
	if changes := f.plan(t); len(changes) != 0 {
		t.Errorf("second plan = %v", changes)
	}
}

func TestApplyOpensVaultOnceAndLocksLast(t *testing.T) {
	f := setup(t)
	f.module.Secrets.Age = append(f.module.Secrets.Age, config.AgeFile{Dst: "~/keys/other.txt", Keys: []config.AgeKey{
		{Bitwarden: "note one", PublicKey: f.one.Recipient().String()},
	}})
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	calls := f.bwCalls()
	unlocks := 0
	for _, c := range calls {
		if c == "bw unlock --raw" {
			unlocks++
		}
	}
	if unlocks != 1 {
		t.Errorf("unlocked %d times: %v", unlocks, calls)
	}
	if calls[len(calls)-1] != "bw lock" {
		t.Errorf("last call = %q", calls[len(calls)-1])
	}
}

func TestSessionTravelsOnlyInTheEnvironment(t *testing.T) {
	f := setup(t)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	sessionCalls := 0
	for _, c := range f.fake.Calls {
		if strings.Contains(c.String(), token) {
			t.Errorf("token in the command line: %s", c)
		}
		if slices.Contains(c.Env, "BW_SESSION="+token) {
			sessionCalls++
		}
		if len(c.Args) > 0 && c.Args[0] == "unlock" && !c.Terminal {
			t.Error("bw unlock must run in terminal mode")
		}
		if c.Stream {
			t.Errorf("%s streams its stdout", c)
		}
	}
	if sessionCalls != 4 {
		t.Errorf("BW_SESSION on %d calls, want sync, two gets and lock", sessionCalls)
	}
}

func TestExistingContentStaysAndOnlyMissingKeysAreRead(t *testing.T) {
	f := setup(t)
	other := identity(t)
	existing := "# mine\n" + other.String() + "\n" + f.one.String()
	f.write(t, existing, 0o600)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if want := existing + "\n" + f.two.String() + "\n"; f.read(t) != want {
		t.Error("existing content should stay in place with the new key appended")
	}
	if f.fake.Ran("bw get notes note one") {
		t.Error("read a note for a key the file already holds")
	}
}

func TestWrongOrMissingNoteLeavesTheFileAlone(t *testing.T) {
	for _, test := range []struct {
		name, want string
		note       runner.Result
	}{
		{"another key", "doesn't hold the key", runner.Result{Stdout: "OTHER"}},
		{"not a key", "doesn't hold the key", runner.Result{Stdout: "hello\n"}},
		{"missing", "Not found.", runner.Result{ExitCode: 1, Stderr: "Not found.\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			original := f.one.String() + "\n"
			f.write(t, original, 0o600)
			note := test.note
			if note.Stdout == "OTHER" {
				note.Stdout = identity(t).String()
			}
			f.over["bw get notes note two"] = note
			err := f.apply(t)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "unchanged") {
				t.Fatalf("err = %v", err)
			}
			if f.read(t) != original {
				t.Error("the file changed")
			}
			entries, err := os.ReadDir(filepath.Dir(f.keysPath))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("left behind: %v", entries)
			}
			calls := f.bwCalls()
			if calls[len(calls)-1] != "bw lock" {
				t.Errorf("a failure must still lock, last call = %q", calls[len(calls)-1])
			}
		})
	}
}

func TestOneWrongNoteWritesNothingForThatFile(t *testing.T) {
	f := setup(t)
	f.over["bw get notes note two"] = runner.Result{Stdout: "hello"}
	if err := f.apply(t); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(f.keysPath); !os.IsNotExist(err) {
		t.Errorf("the first key was written: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(f.keysPath)); !os.IsNotExist(err) {
		t.Error("the directory was created for nothing")
	}
}

func TestCorruptFileIsKept(t *testing.T) {
	f := setup(t)
	f.write(t, "AGE-SECRET-KEY-1BROKEN\nnot a key\n", 0o600)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.read(t), "AGE-SECRET-KEY-1BROKEN\nnot a key\n") {
		t.Error("lines zakwas can't read must stay")
	}
}

func TestNoTerminalFailsBeforeStartingBw(t *testing.T) {
	f := setup(t)
	f.module.Interactive = false
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("err = %v", err)
	}
	if calls := f.bwCalls(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
}

func TestBwNotInstalled(t *testing.T) {
	f := setup(t)
	f.fake.Missing("bw")
	if err := f.apply(t); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSignedOutLogsIn(t *testing.T) {
	f := setup(t)
	f.over["bw status"] = runner.Result{Stdout: `{"status":"unauthenticated"}`}
	f.over["bw login --raw"] = runner.Result{Stdout: token}
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if !f.fake.Ran("bw login --raw") || f.fake.Ran("bw unlock") {
		t.Errorf("calls = %v", f.bwCalls())
	}
}

func TestAlreadyUnlockedStaysUnlocked(t *testing.T) {
	f := setup(t)
	f.over["bw status"] = runner.Result{Stdout: `{"status":"unlocked"}`}
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if f.fake.Ran("bw unlock") || f.fake.Ran("bw lock") {
		t.Errorf("a session the user opened should stay theirs: %v", f.bwCalls())
	}
}

func TestFailedUnlockStopsWithoutLocking(t *testing.T) {
	f := setup(t)
	f.over["bw unlock --raw"] = runner.Result{ExitCode: 1, Stdout: token}
	err := f.apply(t)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("err = %v", err)
	}
	if f.fake.Ran("bw get") || f.fake.Ran("bw lock") {
		t.Errorf("calls = %v", f.bwCalls())
	}
}

func TestLockFailureIsReported(t *testing.T) {
	f := setup(t)
	f.over["bw lock"] = runner.Result{ExitCode: 1}
	if err := f.apply(t); err == nil || !strings.Contains(err.Error(), "run bw lock") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(f.read(t), f.two.String()) {
		t.Error("the keys should still be written")
	}
}

func TestCancelledRunStillLocks(t *testing.T) {
	f := setup(t)
	f.over["bw get notes note one"] = runner.Result{ExitCode: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changes := f.plan(t)
	if err := changes[len(changes)-1].Apply(ctx); err == nil {
		t.Fatal("want an error")
	}
	calls := f.bwCalls()
	if calls[len(calls)-1] != "bw lock" {
		t.Errorf("last call = %q", calls[len(calls)-1])
	}
}

func TestWideModeIsNarrowedWithoutTheVault(t *testing.T) {
	f := setup(t)
	f.write(t, f.one.String()+"\n"+f.two.String()+"\n", 0o644)
	changes := f.plan(t)
	if len(changes) != 1 || changes[0].From != "644" || changes[0].To != "600" {
		t.Fatalf("changes = %+v", changes)
	}
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.keysPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
	if calls := f.bwCalls(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
	if changes := f.plan(t); len(changes) != 0 {
		t.Errorf("second plan = %v", changes)
	}
}

func TestSymlinkIsRefused(t *testing.T) {
	f := setup(t)
	target := filepath.Join(f.home, "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.keysPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.keysPath); err != nil {
		t.Skip(err)
	}
	if _, err := f.module.Plan(context.Background()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoSecretReachesAnyOutput(t *testing.T) {
	f := setup(t)
	secrets := []string{f.one.String(), f.two.String(), token, "me@example.com"}
	f.write(t, f.one.String()+"\n", 0o644)

	check := func(what, text string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s leaks %s", what, s[:8])
			}
		}
	}
	plan := engine.Build(context.Background(), []engine.Module{f.module})
	var rendered bytes.Buffer
	if err := engine.Render(&rendered, plan, engine.Style{}); err != nil {
		t.Fatal(err)
	}
	check("the plan", rendered.String())
	doc, err := json.Marshal(plan.JSONModules(true))
	if err != nil {
		t.Fatal(err)
	}
	check("the JSON plan", string(doc))

	for _, note := range []runner.Result{
		{Stdout: "hello"},
		{ExitCode: 1, Stdout: f.two.String()},
	} {
		f.over["bw get notes note two"] = note
		err := f.apply(t)
		if err == nil {
			t.Fatal("want an error")
		}
		check("an error", err.Error())
	}
}

func TestNoBackupOfAKeyFile(t *testing.T) {
	f := setup(t)
	f.write(t, f.one.String()+"\n", 0o600)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(f.keysPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "keys.txt" {
		t.Errorf("entries = %v", entries)
	}
}

func TestKeyAddedDuringThePromptStays(t *testing.T) {
	f := setup(t)
	f.write(t, "# mine\n", 0o600)
	added := identity(t).String()
	f.during["bw unlock --raw"] = func() { f.write(t, "# mine\n"+added+"\n", 0o600) }
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	content := f.read(t)
	for _, want := range []string{"# mine\n", added, f.one.String(), f.two.String()} {
		if !strings.Contains(content, want) {
			t.Errorf("lost %q", want[:6])
		}
	}
}

func TestSameKeyAddedDuringThePromptIsNotDoubled(t *testing.T) {
	f := setup(t)
	f.during["bw get notes note two"] = func() { f.write(t, f.one.String()+"\n"+f.two.String()+"\n", 0o600) }
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	if want := f.one.String() + "\n" + f.two.String() + "\n"; f.read(t) != want {
		t.Error("the keys should appear once each")
	}
}

func TestKeyRemovedDuringThePromptStopsTheRestore(t *testing.T) {
	f := setup(t)
	original := f.one.String() + "\n"
	f.write(t, original, 0o600)
	f.during["bw get notes note two"] = func() { f.write(t, "# emptied\n", 0o600) }
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "changed during the restore") {
		t.Fatalf("err = %v", err)
	}
	if f.read(t) != "# emptied\n" {
		t.Error("the file should stay as the user left it")
	}
}

func TestSymlinkedParentIsRefused(t *testing.T) {
	f := setup(t)
	other := filepath.Join(f.home, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Dir(f.keysPath)); err != nil {
		t.Skip(err)
	}
	_, err := f.module.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Errorf("wrote through the link: %v", entries)
	}
}
