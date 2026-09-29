package mise

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/engine/enginetest"
	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const pins = "[tools]\njq = \"1.8.2\"\n"

// newModule sets up a repo config and an installed copy that match, as after
// a converged apply.
func newModule(t *testing.T) (*Module, *runnertest.Fake) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "dotfiles/mise/config.toml"), pins)
	writeFile(t, filepath.Join(home, ".config/mise/config.toml"), pins)
	fake := runnertest.New()
	return &Module{
		Mise:   config.Mise{Config: "dotfiles/mise/config.toml"},
		Paths:  config.Paths{Home: home, Root: root},
		Runner: fake,
	}, fake
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func targets(changes []engine.Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, string(c.Action)+" "+c.Target)
	}
	return out
}

func TestPlanAndApply(t *testing.T) {
	m, fake := newModule(t)
	fake.OnOK("mise ls --global --missing --json", `{
  "kubectl": [{"version": "1.34.1", "installed": false}],
  "jq": [{"version": "1.7.1", "installed": false}, {"version": "1.6", "installed": true}]
}`)
	fake.OnOK("mise install --yes", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, string(c.Action)+" "+c.Target)
	}
	want := []string{"+ jq@1.7.1", "+ kubectl@1.34.1", "! mise install"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	wantEnv := []string{"MISE_GLOBAL_CONFIG_FILE=" + m.Paths.Src(m.Mise.Config)}
	for _, c := range fake.Calls {
		if c.Dir != "/" || !reflect.DeepEqual(c.Env, wantEnv) {
			t.Errorf("%s: dir %q env %v; must target the repo config from /", c, c.Dir, c.Env)
		}
	}
	if !fake.Calls[len(fake.Calls)-1].Stream {
		t.Error("install should stream output")
	}
}

func TestNothingMissing(t *testing.T) {
	m, fake := newModule(t)
	fake.OnOK("mise ls --global --missing --json", "{}")
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Errorf("changes = %v, err = %v", changes, err)
	}
}

func TestPlanErrors(t *testing.T) {
	tests := map[string]runner.Result{
		"command fails": {ExitCode: 1, Stderr: "config not trusted"},
		"bad json":      {Stdout: "not json"},
	}
	for name, res := range tests {
		t.Run(name, func(t *testing.T) {
			m, fake := newModule(t)
			fake.On("mise ls --global --missing --json", res)
			if _, err := m.Plan(context.Background()); err == nil {
				t.Error("expected error")
			}
		})
	}
}

const prunable = `{
  "pipx": [{"version": "1.17.6", "installed": true, "active": false}],
  "github:can1357/oh-my-pi": [{"version": "18.3.4", "installed": true, "active": false}]
}`

func TestPrune(t *testing.T) {
	m, fake := newModule(t)
	m.Mise.Prune = true
	fake.OnOK("mise ls --global --missing --json", "{}")
	fake.OnOK("mise ls --prunable --json", prunable)
	fake.OnOK("mise prune --yes", "")

	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, string(c.Action)+" "+c.Target)
	}
	want := []string{"- github:can1357/oh-my-pi@18.3.4", "- pipx@1.17.6", "! mise prune"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if err := enginetest.Apply(context.Background(), engine.Plan{{Changes: changes}}); err != nil {
		t.Fatal(err)
	}
	if !fake.Ran("mise prune --yes") {
		t.Errorf("prune not applied; ran %v", fake.Lines())
	}
}

func TestPruneDisabledNeverRuns(t *testing.T) {
	m, fake := newModule(t)
	fake.OnOK("mise ls --global --missing --json", "{}")
	if _, err := m.Plan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.Ran("mise prune") {
		t.Error("prune ran although disabled")
	}
}

func TestPruneNothingToDo(t *testing.T) {
	m, fake := newModule(t)
	m.Mise.Prune = true
	fake.OnOK("mise ls --global --missing --json", "{}")
	fake.OnOK("mise ls --prunable --json", "{}")
	changes, err := m.Plan(context.Background())
	if err != nil || len(changes) != 0 {
		t.Errorf("changes = %v, err = %v", changes, err)
	}
}

func TestPruneScheduledWhenInstalling(t *testing.T) {
	m, fake := newModule(t)
	m.Mise.Prune = true
	fake.OnOK("mise ls --global --missing --json", `{"jq":[{"version":"1.8.3","installed":false}]}`)
	fake.OnOK("mise ls --prunable --json", "{}")
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ jq@1.8.3", "! mise install", "! mise prune"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v (prune must follow a bump in the same apply)", got, want)
	}
}

func TestPruneReportsBothPrunableAndSuperseded(t *testing.T) {
	m, fake := newModule(t)
	m.Mise.Prune = true
	fake.OnOK("mise ls --global --missing --json", `{"jq":[{"version":"1.8.3","installed":false}]}`)
	fake.OnOK("mise ls --prunable --json", prunable)
	changes, err := m.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"+ jq@1.8.3", "! mise install", "- github:can1357/oh-my-pi@18.3.4", "- pipx@1.17.6", "! mise prune"}
	if got := targets(changes); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if changes[len(changes)-1].Detail == "" {
		t.Error("prune must mention the versions the bump supersedes, not only those listed")
	}
}

func TestPruneScheduledWhenInstalledConfigStale(t *testing.T) {
	tests := map[string]func(t *testing.T, m *Module){
		"installed copy differs": func(t *testing.T, m *Module) {
			writeFile(t, m.Paths.Dst(InstalledConfig), "[tools]\njq = \"1.7.1\"\nkind = \"0.20.0\"\n")
		},
		"installed copy missing": func(t *testing.T, m *Module) {
			if err := os.Remove(m.Paths.Dst(InstalledConfig)); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			m, fake := newModule(t)
			m.Mise.Prune = true
			setup(t, m)
			fake.OnOK("mise ls --global --missing --json", "{}")
			fake.OnOK("mise ls --prunable --json", "{}")
			changes, err := m.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := targets(changes); !reflect.DeepEqual(got, []string{"! mise prune"}) {
				t.Fatalf("got %v", got)
			}
			if changes[0].Detail == "" {
				t.Error("scheduled prune should explain why")
			}
		})
	}
}

func TestMergeBumps(t *testing.T) {
	changes := mergeBumps([]engine.Change{
		{Action: engine.Create, Target: "ruff@0.16.9"},
		{Action: engine.Create, Target: "jq@1.8.2"},
		{Action: engine.Create, Target: "node@24"},
		{Action: engine.Create, Target: "node@22"},
		{Action: engine.Run, Target: "mise install"},
		{Action: engine.Remove, Target: "ruff@0.16.2", Destructive: true},
		{Action: engine.Remove, Target: "node@20", Destructive: true},
		{Action: engine.Remove, Target: "pnpm@10", Destructive: true},
		{Action: engine.Run, Target: "mise prune", Destructive: true},
	})
	var got []string
	for _, c := range changes {
		got = append(got, c.String())
	}
	want := []string{
		"~ ruff (0.16.2 → 0.16.9)", "+ jq@1.8.2", "+ node@24", "+ node@22", "! mise install",
		"- node@20", "- pnpm@10", "! mise prune",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
