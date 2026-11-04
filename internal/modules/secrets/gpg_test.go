package secrets

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/testutil"
)

const (
	listCmd    = "gpg --batch --list-secret-keys --with-colons --with-subkey-fingerprints"
	importCmd  = "gpg --batch --import"
	primaryFpr = "904F9010E4362132E576865EC25DFDF396ADF455"
	subkeyFpr  = "4C87E4377EC834C405AE619989B58A1317507C65"
	cardFpr    = "F8714526C8ED3BFD807C10A1683BF754A0B005CD"
	noteName   = "gpg main secret keys"
	armor      = "-----BEGIN PGP PRIVATE KEY BLOCK-----\nNOTE-BODY-MARKER\n-----END PGP PRIVATE KEY BLOCK-----\n"
)

// listing is what gpg --with-colons prints for a primary key held in the
// keyring, a software subkey, and a subkey that lives on a smartcard.
const listing = `sec:u:255:22:C25DFDF396ADF455:1764115200:::u:::cESCA:::+:::ed25519:::0:
fpr:::::::::` + primaryFpr + `:
grp:::::::::AAAA:
uid:u::::1764115200::HASH::Marcin <m@example.com>::::::::::0:
ssb:u:255:22:683BF754A0B005CD:1764115200::::::s:::D2760001240100000006208782670000:::ed25519::
fpr:::::::::` + cardFpr + `:
ssb:u:255:22:89B58A1317507C65:1764115200:1795651200:::::s:::+:::ed25519::
fpr:::::::::` + subkeyFpr + `:
`

func TestParseSecretKeys(t *testing.T) {
	tests := []struct {
		name    string
		listing string
		want    []string
	}{
		{"empty keyring", "", nil},
		{"primary, software subkey and card subkey", listing, []string{primaryFpr, subkeyFpr}},
		{"stub without the secret", "sec:u:255:22:AAAA:1::::::cESCA:::#:::ed25519:::0:\nfpr:::::::::" + primaryFpr + ":\n", nil},
		{"public key only", "pub:u:255:22:AAAA:1::::::cESCA:::::ed25519:::0:\nfpr:::::::::" + primaryFpr + ":\n", nil},
		{"lowercase fingerprint", "sec:u:255:22:AAAA:1::::::cESCA:::+:::ed25519:::0:\nfpr:::::::::" + strings.ToLower(primaryFpr) + ":\n", []string{primaryFpr}},
		{"crlf", strings.ReplaceAll(listing, "\n", "\r\n"), []string{primaryFpr, subkeyFpr}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for fpr := range parseSecretKeys(tt.listing) {
				got = append(got, fpr)
			}
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

type gpgFixture struct {
	fake   *runnertest.Fake
	module *Module
}

// setupGPG scripts a keyring that is empty until the import and holds the
// listing afterwards; the last queued answer repeats.
func setupGPG(t *testing.T, fingerprint string) *gpgFixture {
	t.Helper()
	testutil.SkipOnWindows(t, "the module is not supported there")
	fake := runnertest.New()
	fake.OnOK("bw status", `{"status":"locked"}`)
	fake.OnOK("bw unlock --raw", token+"\n")
	fake.OnOK("bw sync", "")
	fake.OnOK("bw lock", "")
	fake.OnOK("bw get notes "+noteName, armor)
	fake.OnOK(importCmd, "")
	fake.OnOK(listCmd, "")
	fake.OnOK(listCmd, "")
	fake.OnOK(listCmd, listing)
	return &gpgFixture{fake: fake, module: &Module{
		Secrets:     config.Secrets{GPG: []config.GPGKey{{Bitwarden: noteName, Fingerprint: fingerprint}}},
		Paths:       config.Paths{Home: t.TempDir()},
		Runner:      fake,
		Interactive: true,
	}}
}

func (f *gpgFixture) plan(t *testing.T) []engine.Change {
	t.Helper()
	changes, err := f.module.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func (f *gpgFixture) apply(t *testing.T) error {
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

func TestGPGPlanNamesTheNoteWithoutStartingBw(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	changes := f.plan(t)
	if len(changes) != 2 {
		t.Fatalf("changes = %v", changes)
	}
	if changes[0].Target != "gpg key 89B58A1317507C65" || !strings.Contains(changes[0].Detail, noteName) {
		t.Errorf("entry change = %+v", changes[0])
	}
	if changes[1].Target != "restore GPG keys from Bitwarden" || changes[1].Apply == nil {
		t.Errorf("run change = %+v", changes[1])
	}
	if got := f.fake.Lines(); !slices.Equal(got, []string{listCmd}) {
		t.Errorf("plan ran %v", got)
	}
}

func TestGPGApplyImportsThenConverges(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	lines := f.fake.Lines()
	if lines[len(lines)-1] != "bw lock" {
		t.Errorf("last call = %q in %v", lines[len(lines)-1], lines)
	}
	var imported *runner.Cmd
	for i, c := range f.fake.Calls {
		if c.String() == importCmd {
			imported = &f.fake.Calls[i]
		}
	}
	if imported == nil || imported.Stdin != armor {
		t.Fatalf("the note should reach gpg on stdin, got %+v", imported)
	}
	for _, c := range f.fake.Calls {
		if strings.Contains(c.String(), "NOTE-BODY-MARKER") || strings.Contains(strings.Join(c.Env, " "), "NOTE-BODY-MARKER") {
			t.Errorf("the note leaked into %q", c)
		}
	}
	if changes := f.plan(t); len(changes) != 0 {
		t.Errorf("second plan = %v", changes)
	}
}

func TestGPGHeldKeyNeedsNothing(t *testing.T) {
	f := setupGPG(t, strings.ToLower(primaryFpr))
	f.fake = runnertest.New().OnOK(listCmd, listing)
	f.module.Runner = f.fake
	if changes := f.plan(t); len(changes) != 0 {
		t.Errorf("plan = %v", changes)
	}
	if got := f.fake.Lines(); !slices.Equal(got, []string{listCmd}) {
		t.Errorf("plan ran %v", got)
	}
}

func TestGPGSmartcardKeyIsNotHeld(t *testing.T) {
	f := setupGPG(t, cardFpr)
	f.fake = runnertest.New().OnOK(listCmd, listing)
	f.module.Runner = f.fake
	if changes := f.plan(t); len(changes) != 2 {
		t.Errorf("a card subkey should still be missing, got %v", changes)
	}
}

func TestGPGImportThatLeavesTheKeyMissingFails(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New()
	f.fake.OnOK("bw status", `{"status":"unlocked"}`).OnOK("bw sync", "").
		OnOK("bw get notes "+noteName, armor).OnOK(importCmd, "").OnOK(listCmd, "")
	f.module.Runner = f.fake
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), subkeyFpr) || !strings.Contains(err.Error(), noteName) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "NOTE-BODY-MARKER") {
		t.Error("the note leaked into the error")
	}
}

func TestGPGFailedImportReportsOnlyTheExitCode(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New()
	f.fake.OnOK("bw status", `{"status":"unlocked"}`).OnOK("bw sync", "").
		OnOK("bw get notes "+noteName, armor).OnOK(listCmd, "").
		On(importCmd, runner.Result{ExitCode: 2, Stderr: "gpg: NOTE-BODY-MARKER", Stdout: "NOTE-BODY-MARKER"})
	f.module.Runner = f.fake
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "exit 2") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "NOTE-BODY-MARKER") {
		t.Error("gpg's output leaked into the error")
	}
}

func TestGPGEmptyNoteFailsBeforeImporting(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New()
	f.fake.OnOK("bw status", `{"status":"unlocked"}`).OnOK("bw sync", "").
		OnOK("bw get notes "+noteName, "\n").OnOK(listCmd, "")
	f.module.Runner = f.fake
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("err = %v", err)
	}
	if f.fake.Ran("gpg --batch --import") {
		t.Error("an empty note should not reach gpg")
	}
}

func TestGPGMissingNoteIsReported(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New()
	f.fake.OnOK("bw status", `{"status":"unlocked"}`).OnOK("bw sync", "").OnOK(listCmd, "").
		On("bw get notes "+noteName, runner.Result{ExitCode: 1, Stderr: "Not found."})
	f.module.Runner = f.fake
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "Not found.") {
		t.Fatalf("err = %v", err)
	}
}

func TestGPGNeedsATerminalBeforeStartingBw(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.module.Interactive = false
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "in a terminal") {
		t.Fatalf("err = %v", err)
	}
	if f.fake.Ran("bw") {
		t.Errorf("bw started: %v", f.fake.Lines())
	}
}

func TestGPGNotInstalledStopsBeforeStartingBw(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New().Missing("gpg")
	f.module.Runner = f.fake
	if changes := f.plan(t); len(changes) != 2 {
		t.Errorf("plan = %v", changes)
	}
	err := f.apply(t)
	if err == nil || !strings.Contains(err.Error(), "gpg isn't installed") {
		t.Fatalf("err = %v", err)
	}
	if len(f.fake.Lines()) != 0 {
		t.Errorf("ran %v", f.fake.Lines())
	}
}

func TestGPGListFailureFailsThePlan(t *testing.T) {
	f := setupGPG(t, subkeyFpr)
	f.fake = runnertest.New().On(listCmd, runner.Result{ExitCode: 2, Stderr: "gpg: NOTE-BODY-MARKER"})
	f.module.Runner = f.fake
	_, err := f.module.Plan(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exit 2") || strings.Contains(err.Error(), "NOTE-BODY-MARKER") {
		t.Fatalf("err = %v", err)
	}
}

func TestGPGAndAgeShareOneVaultSession(t *testing.T) {
	f := setup(t)
	g := setupGPG(t, subkeyFpr)
	f.fake.OnOK(listCmd, "")
	f.fake.OnOK(listCmd, "")
	f.fake.OnOK("bw get notes "+noteName, armor)
	f.fake.OnOK(importCmd, "")
	f.fake.OnOK(listCmd, listing)
	f.module.Secrets.GPG = g.module.Secrets.GPG
	if err := f.apply(t); err != nil {
		t.Fatal(err)
	}
	unlocks := 0
	for _, line := range f.fake.Lines() {
		if line == "bw unlock --raw" {
			unlocks++
		}
	}
	if unlocks != 1 {
		t.Errorf("unlocked %d times: %v", unlocks, f.fake.Lines())
	}
}
