package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

// offlineCfg has a module of every kind, so an offline check that started
// one would fail against the scripted runner.
const offlineCfg = `files: [{src: dotfiles/zshrc, dst: ~/.zshenv}]
links: [{src: dotfiles/zshrc, dst: ~/.zshrc}]
templates:
  vars: {name: zakwas}
  files: [{src: dotfiles/greeting.tmpl, dst: ~/.greeting}]
brew: {file: Brewfile}
mise: {config: dotfiles/mise.toml}
commands:
  - {name: once, check: "test -e /nonexistent", run: "true"}
defaults:
  - {domain: com.apple.dock, key: autohide, value: true}
system: {sudoTouchID: true}
`

func setupOffline(t *testing.T) (Env, string) {
	t.Helper()
	env, home := setup(t, offlineCfg)
	for name, body := range map[string]string{
		"dotfiles/greeting.tmpl": "hello {{ .Vars.name }}\n",
		"dotfiles/mise.toml":     "[tools]\n",
		"Brewfile":               "",
	} {
		if err := os.WriteFile(filepath.Join(env.Cwd, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return env, home
}

// applyOwned converges only what an offline check looks at.
func applyOwned(t *testing.T, env Env) {
	t.Helper()
	if r := invoke(env, "", "apply", "-y", "--only", "files,links,templates"); r.code != ExitOK {
		t.Fatalf("apply: %+v", r)
	}
}

func TestOfflineCheckExitCodes(t *testing.T) {
	env, home := setupOffline(t)
	fake := runnertest.New()
	env.Runner = fake

	r := invoke(env, "", "check", "--offline")
	if r.code != ExitDrift || !strings.Contains(r.stdout, "+ ~/.zshrc") {
		t.Fatalf("before apply: %+v", r)
	}

	applyOwned(t, env)
	fake.Calls = nil
	r = invoke(env, "", "check", "--offline")
	if r.code != ExitOK {
		t.Fatalf("after apply: %+v", r)
	}
	for _, skipped := range []string{"system", "brew", "mise", "agents", "commands", "defaults"} {
		if !strings.Contains(r.stdout, "not checked: ") || !strings.Contains(r.stdout, skipped) {
			t.Errorf("output does not name skipped %q:\n%s", skipped, r.stdout)
		}
	}

	if err := os.Chmod(filepath.Join(home, ".zshenv"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshenv"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = invoke(env, "", "check", "--offline")
	if r.code != ExitDrift || !strings.Contains(r.stdout, "edited in place") {
		t.Errorf("after an edit: %+v", r)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("offline check started commands: %v", fake.Lines())
	}
}

func TestOfflineCheckStartsNoCommand(t *testing.T) {
	env, _ := setupOffline(t)
	fake := runnertest.New()
	env.Runner = fake
	applyOwned(t, env)
	fake.Calls = nil

	if r := invoke(env, "", "check", "--offline", "--json"); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("commands started: %v", fake.Lines())
	}
}

func TestOfflineCheckJSON(t *testing.T) {
	env, _ := setupOffline(t)
	r := invoke(env, "", "check", "--offline", "--json")
	if r.code != ExitDrift {
		t.Fatalf("%+v", r)
	}
	var doc planDoc
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if !doc.Offline || !strings.Contains(strings.Join(doc.Skipped, ","), "brew") {
		t.Errorf("offline = %v, skipped = %v", doc.Offline, doc.Skipped)
	}
	var names []string
	for _, m := range doc.Modules {
		names = append(names, m.Name)
	}
	if got := strings.Join(names, ","); got != "files,links,templates" {
		t.Errorf("modules = %s", got)
	}
	if full := invoke(env, "", "plan", "--json"); strings.Contains(full.stdout, `"offline"`) {
		t.Error("a plan without --offline says it is offline")
	}
}

func TestOfflineFlagConflicts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		msg  string
	}{
		{"plan", []string{"plan", "--offline"}, "-offline only works with check"},
		{"apply", []string{"apply", "--offline", "-y"}, "-offline only works with check"},
		{"module needing commands", []string{"check", "--offline", "--only", "files,brew"}, `module "brew" can't run with -offline`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setupOffline(t)
			r := invoke(env, "", tt.args...)
			if r.code != ExitUsage || !strings.Contains(r.stderr, tt.msg) {
				t.Errorf("%+v", r)
			}
		})
	}
}

func TestOfflineCheckWithOnlyOfflineModules(t *testing.T) {
	env, _ := setupOffline(t)
	r := invoke(env, "", "check", "--offline", "--only", "links")
	if r.code != ExitDrift || strings.Contains(r.stdout, ".greeting") {
		t.Errorf("%+v", r)
	}
}
