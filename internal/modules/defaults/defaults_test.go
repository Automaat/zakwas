package defaults

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func TestEncode(t *testing.T) {
	tests := []struct {
		in   any
		want Value
	}{
		{true, Value{"bool", "1"}},
		{false, Value{"bool", "0"}},
		{15, Value{"int", "15"}},
		{0.5, Value{"float", "0.5"}},
		{2.0, Value{"float", "2"}},
		{"~/Documents/screenshots", Value{"string", "~/Documents/screenshots"}},
	}
	for _, tt := range tests {
		got, err := Encode(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("Encode(%#v) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	if _, err := Encode([]any{1}); err == nil {
		t.Error("expected error for array")
	}
}

// state maps "domain key" to the current (type, value) as defaults prints it;
// missing entries behave like an unset key.
func fakeDefaults(state map[string][2]string) *runnertest.Fake {
	fake := runnertest.New()
	for k, v := range state {
		fake.OnOK("defaults read-type "+k, "Type is "+v[0]+"\n")
		fake.OnOK("defaults read "+k, v[1]+"\n")
	}
	return fake
}

// noActivateSettings makes the plan independent of the host's macOS.
func noActivateSettings(t *testing.T) {
	t.Helper()
	old := ActivateSettings
	ActivateSettings = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { ActivateSettings = old })
}

func TestPlan(t *testing.T) {
	noActivateSettings(t)
	fake := fakeDefaults(map[string][2]string{
		"com.apple.dock autohide":          {"boolean", "1"},
		"com.apple.dock show-recents":      {"boolean", "1"},
		"NSGlobalDomain KeyRepeat":         {"integer", "2"},
		"NSGlobalDomain InitialKeyRepeat":  {"boolean", "1"},
		"com.apple.finder FXPreferredView": {"array", "()"},
		"x.float f":                        {"float", "0.50"},
	})
	fake.On("defaults read-type com.apple.dock tilesize", runner.Result{ExitCode: 1, Stderr: "does not exist"})

	m := &Module{Runner: fake, Defaults: []config.Default{
		{Domain: "com.apple.dock", Key: "autohide", Value: true},
		{Domain: "com.apple.dock", Key: "show-recents", Value: false},
		{Domain: "com.apple.dock", Key: "tilesize", Value: 48},
		{Domain: "NSGlobalDomain", Key: "KeyRepeat", Value: 2},
		{Domain: "NSGlobalDomain", Key: "InitialKeyRepeat", Value: 1},
		{Domain: "com.apple.finder", Key: "FXPreferredView", Value: "Nlsv"},
		{Domain: "x.float", Key: "f", Value: 0.5},
		{Domain: "x.custom", Key: "k", Value: "v", Restart: "cfprefsd"},
	}}
	fake.On("defaults read-type x.custom k", runner.Result{ExitCode: 1})

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, c.String())
	}
	want := []string{
		"~ com.apple.dock show-recents (bool:1 → bool:0)",
		"~ com.apple.dock tilesize (unset → int:48)",
		"~ NSGlobalDomain InitialKeyRepeat (bool:1 → int:1)",
		"~ com.apple.finder FXPreferredView (array: → string:Nlsv)",
		"~ x.custom k (unset → string:v)",
		"! killall Dock",
		"! killall Finder",
		"! killall cfprefsd",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got:\n%v\nwant:\n%v", got, want)
	}
}

func TestApply(t *testing.T) {
	noActivateSettings(t)
	fake := fakeDefaults(map[string][2]string{"com.apple.dock autohide": {"boolean", "0"}})
	fake.On("defaults read-type NSGlobalDomain KeyRepeat", runner.Result{ExitCode: 1})
	fake.OnOK("defaults write com.apple.dock autohide -bool true", "")
	fake.OnOK("defaults write NSGlobalDomain KeyRepeat -int 2", "")
	fake.On("killall Dock", runner.Result{ExitCode: 1, Stderr: "No matching processes"})

	m := &Module{Runner: fake, Defaults: []config.Default{
		{Domain: "com.apple.dock", Key: "autohide", Value: true},
		{Domain: "NSGlobalDomain", Key: "KeyRepeat", Value: 2},
	}}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatalf("apply: %v (killall of a stopped process must not fail)", err)
	}
	for _, want := range []string{"defaults write com.apple.dock autohide -bool true", "defaults write NSGlobalDomain KeyRepeat -int 2", "killall Dock"} {
		if !fake.Ran(want) {
			t.Errorf("did not run %q; ran %v", want, fake.Lines())
		}
	}
}

func TestWriteFailureSurfaces(t *testing.T) {
	fake := runnertest.New()
	fake.On("defaults read-type d k", runner.Result{ExitCode: 1})
	fake.On("defaults write d k -string v", runner.Result{ExitCode: 1, Stderr: "Could not write domain"})
	m := &Module{Runner: fake, Defaults: []config.Default{{Domain: "d", Key: "k", Value: "v"}}}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err == nil {
		t.Error("expected write failure")
	}
}

func TestCurrentHost(t *testing.T) {
	noActivateSettings(t)
	fake := runnertest.New()
	fake.OnOK("defaults -currentHost read-type com.apple.controlcenter BatteryShowPercentage", "Type is boolean\n")
	fake.OnOK("defaults -currentHost read com.apple.controlcenter BatteryShowPercentage", "0\n")
	fake.OnOK("defaults -currentHost write com.apple.controlcenter BatteryShowPercentage -bool true", "")

	m := &Module{Runner: fake, Defaults: []config.Default{
		{Domain: "com.apple.controlcenter", Key: "BatteryShowPercentage", Value: true, CurrentHost: true},
	}}
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].String() != "~ -currentHost com.apple.controlcenter BatteryShowPercentage (bool:0 → bool:1)" {
		t.Fatalf("changes = %v", changes)
	}
	if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if !fake.Ran("defaults -currentHost write") {
		t.Errorf("ran %v", fake.Lines())
	}
}

func TestActivateSettingsAfterGlobalDomainWrite(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "activateSettings")
	if err := os.WriteFile(tool, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	old := ActivateSettings
	ActivateSettings = tool
	t.Cleanup(func() { ActivateSettings = old })

	tests := []struct {
		name   string
		domain string
		want   []string
	}{
		{"global domain", "NSGlobalDomain", []string{"~ NSGlobalDomain k", "! " + tool + " -u"}},
		{"app domain", "com.apple.dock", []string{"~ com.apple.dock k", "! killall Dock"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := runnertest.New().On("defaults read-type "+tt.domain+" k", runner.Result{ExitCode: 1})
			fake.On(tool+" -u", runner.Result{ExitCode: 1})
			fake.OnOK("defaults write "+tt.domain+" k -int 1", "")
			fake.On("killall Dock", runner.Result{})
			m := &Module{Runner: fake, Defaults: []config.Default{{Domain: tt.domain, Key: "k", Value: 1}}}
			changes, err := m.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range changes {
				got = append(got, string(c.Action)+" "+c.Target)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			if err := engine.Apply(context.Background(), func(string) {}, engine.Plan{{Changes: changes}}); err != nil {
				t.Errorf("apply: %v (activateSettings is best effort)", err)
			}
		})
	}
}
