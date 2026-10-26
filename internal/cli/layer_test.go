package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/layers"
)

const filesOnly = "files: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n"

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func layerRepo(t *testing.T, dst, body string) string {
	t.Helper()
	return writeFiles(t, map[string]string{
		"zakwas.yaml": "files: [{src: payload, dst: " + dst + "}]\n",
		"payload":     body,
	})
}

func mustOK(t *testing.T, env Env, args ...string) result {
	t.Helper()
	r := invoke(env, "", args...)
	if r.code != ExitOK {
		t.Fatalf("zakwas %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

func TestLayerAddListRemoveAndConverge(t *testing.T) {
	env, home := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	lab := layerRepo(t, "~/.lab", "lab\n")

	if r := mustOK(t, env, "layer", "list"); !strings.Contains(r.stdout, "No layers registered") {
		t.Errorf("list = %q", r.stdout)
	}
	mustOK(t, env, "layer", "add", "work", work)
	mustOK(t, env, "layer", "add", "lab", filepath.Join(lab, "zakwas.yaml"))
	r := mustOK(t, env, "layer", "list")
	if got := strings.Fields(r.stdout); len(got) != 4 || got[0] != "work" || got[1] != filepath.Join(work, "zakwas.yaml") || got[2] != "lab" {
		t.Errorf("list = %q, want both layers in registration order", r.stdout)
	}

	plan := mustOK(t, env, "plan")
	for _, want := range []string{"~/.zshrc", "~/.work", "~/.lab"} {
		if !strings.Contains(plan.stdout, want) {
			t.Errorf("plan lacks %s:\n%s", want, plan.stdout)
		}
	}
	mustOK(t, env, "apply", "-y")
	for name, want := range map[string]string{".zshrc": "export EDITOR=vim\n", ".work": "work\n", ".lab": "lab\n"} {
		if got, err := os.ReadFile(filepath.Join(home, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v, want %q", name, got, err, want)
		}
	}
	if again := mustOK(t, env, "plan"); strings.Contains(again.stdout, "~/") {
		t.Errorf("second plan is not empty:\n%s", again.stdout)
	}
	mustOK(t, env, "apply", "-y")
	mustOK(t, env, "check")
	for _, name := range []string{".work", ".lab"} {
		if !exists(filepath.Join(home, name)) {
			t.Errorf("a second apply removed %s", name)
		}
	}

	mustOK(t, env, "layer", "remove", "work")
	after := mustOK(t, env, "plan")
	if !strings.Contains(after.stdout, "- ~/.work") || strings.Contains(after.stdout, "~/.lab") {
		t.Errorf("plan after remove should list only ~/.work as a removal:\n%s", after.stdout)
	}
	mustOK(t, env, "apply", "-y")
	if exists(filepath.Join(home, ".work")) || !exists(filepath.Join(home, ".lab")) {
		t.Error("removing a layer should drop only its files")
	}
}

func TestLayerStoredInRegistryKeyedByRoot(t *testing.T) {
	env, home := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)

	data, err := os.ReadFile(layers.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int
		Roots   map[string][]struct{ Name, Path string }
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(env.Cwd)
	got := doc.Roots[root]
	if doc.Version != 1 || len(got) != 1 || got[0].Name != "work" || got[0].Path != filepath.Join(work, "zakwas.yaml") {
		t.Errorf("registry = %s", data)
	}
}

func TestLayerOnlyAppliesToItsRoot(t *testing.T) {
	env, _ := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)
	other := writeFiles(t, map[string]string{"zakwas.yaml": "files: [{src: o, dst: ~/.other}]\n", "o": "o\n"})

	r := mustOK(t, env, "-c", filepath.Join(other, "zakwas.yaml"), "plan")

	if strings.Contains(r.stdout, "~/.work") || !strings.Contains(r.stdout, "~/.other") {
		t.Errorf("a config registered nothing for got the layer:\n%s", r.stdout)
	}
	list := mustOK(t, env, "layer", "list", "-c", filepath.Join(other, "zakwas.yaml"))
	if !strings.Contains(list.stdout, "No layers registered") {
		t.Errorf("list for another root = %q", list.stdout)
	}
}

func TestMissingLayerFailsEveryPlanningCommand(t *testing.T) {
	env, home := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)
	mustOK(t, env, "apply", "-y")
	if err := os.Remove(filepath.Join(work, "zakwas.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"plan"}, {"check"}, {"apply", "-y"}, {"upgrade", "-y"}, {"plan", "-json"}} {
		r := invoke(env, "", args...)
		if r.code != ExitErr {
			t.Errorf("%v: exit %d, want %d\nstdout: %s", args, r.code, ExitErr, r.stdout)
		}
		if r.stdout != "" {
			t.Errorf("%v printed a plan: %s", args, r.stdout)
		}
		if strings.Count(strings.TrimSpace(r.stderr), "\n") != 0 || !strings.Contains(r.stderr, `layer "work"`) || !strings.Contains(r.stderr, "zakwas layer remove work") {
			t.Errorf("%v: stderr = %q, want one line naming the layer and the remove command", args, r.stderr)
		}
	}
	if exists(filepath.Join(home, ".zshrc")) {
		t.Error("a failed apply changed the system")
	}
	if !exists(filepath.Join(home, ".work")) {
		t.Error("a failed run removed the layer's file")
	}

	if r := mustOK(t, env, "layer", "list"); !strings.Contains(r.stdout, "(missing)") {
		t.Errorf("list does not flag the missing file: %q", r.stdout)
	}
	mustOK(t, env, "layer", "remove", "work")
	mustOK(t, env, "plan")
}

func TestInvalidLayerFailsLoadingNamingTheLayer(t *testing.T) {
	env, _ := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)
	if err := os.WriteFile(filepath.Join(work, "zakwas.yaml"), []byte("files: [{src: only}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "plan")

	first, _, _ := strings.Cut(r.stderr, "\n")
	if r.code != ExitErr || !strings.Contains(first, `layer "work"`) || !strings.Contains(first, "zakwas layer remove work") || !strings.Contains(r.stderr, "src and dst are required") {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestLayerAddRejects(t *testing.T) {
	work := layerRepo(t, "~/.work", "work\n")
	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{"an uppercase name", []string{"add", "Work", work}, "layer name", ExitUsage},
		{"a name starting with a dash", []string{"add", "-x", work}, "", ExitUsage},
		{"no path", []string{"add", "work"}, "Usage", ExitUsage},
		{"a path that does not exist", []string{"add", "work", filepath.Join(work, "nope")}, "zakwas: ", ExitErr},
		{"a directory without a config", []string{"add", "work", t.TempDir()}, "zakwas: ", ExitErr},
		{"remove without a name", []string{"remove"}, "Usage", ExitUsage},
		{"an unknown subcommand", []string{"zap"}, "unknown layer command", ExitUsage},
		{"no subcommand", nil, "Usage", ExitUsage},
		{"list with arguments", []string{"list", "x"}, "Usage", ExitUsage},
		{"removing a layer that is not registered", []string{"remove", "work"}, `no layer "work"`, ExitErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, home := setup(t, filesOnly)
			r := invoke(env, "", append([]string{"layer"}, tt.args...)...)
			if r.code != tt.code || !strings.Contains(r.stderr, tt.want) {
				t.Errorf("exit %d, want %d with %q\nstderr: %s", r.code, tt.code, tt.want, r.stderr)
			}
			if exists(layers.Path(home)) {
				t.Error("a rejected command wrote the registry")
			}
		})
	}
}

func TestLayerAddValidatesTheMerge(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"a root-only section", "brew: {file: Brewfile}\n", []string{"can't add layer", "brew is set in a layer"}},
		{"a protect section", "protect: {immutable: true}\n", []string{"protect is set in a layer"}},
		{"an invalid entry", "files: [{src: x}]\n", []string{"can't add layer", "src and dst are required"}},
		{"a destination the main config uses", "files: [{src: payload, dst: ~/.zshrc}]\n", []string{"overlaps"}},
		{"an unreadable yaml", "files: [", []string{"can't add layer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, home := setup(t, filesOnly)
			dir := writeFiles(t, map[string]string{"zakwas.yaml": tt.body, "payload": "x\n"})

			r := invoke(env, "", "layer", "add", "work", dir)

			if r.code != ExitErr {
				t.Errorf("exit %d, want %d", r.code, ExitErr)
			}
			for _, want := range tt.want {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr lacks %q: %s", want, r.stderr)
				}
			}
			if strings.Contains(r.stderr, "zakwas layer remove") {
				t.Errorf("a layer that was never added points at `layer remove`: %s", r.stderr)
			}
			if exists(layers.Path(home)) {
				t.Error("the registry was written")
			}
		})
	}
}

func TestLayerAddTwoLayersClaimingOneDestination(t *testing.T) {
	env, _ := setup(t, filesOnly)
	one := layerRepo(t, "~/.shared", "one\n")
	two := layerRepo(t, "~/.shared", "two\n")
	mustOK(t, env, "layer", "add", "one", one)

	r := invoke(env, "", "layer", "add", "two", two)

	if r.code != ExitErr {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	for _, f := range []string{filepath.Join(one, "zakwas.yaml"), filepath.Join(two, "zakwas.yaml")} {
		if !strings.Contains(r.stderr, f) {
			t.Errorf("stderr does not name %s:\n%s", f, r.stderr)
		}
	}
}

func TestLayerAddRefusesADuplicateNameOrFile(t *testing.T) {
	env, _ := setup(t, filesOnly)
	one := layerRepo(t, "~/.one", "one\n")
	two := layerRepo(t, "~/.two", "two\n")
	mustOK(t, env, "layer", "add", "one", one)

	if r := invoke(env, "", "layer", "add", "one", two); r.code != ExitErr || !strings.Contains(r.stderr, `layer "one" is already registered`) {
		t.Errorf("same name: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := invoke(env, "", "layer", "add", "again", one); r.code != ExitErr || !strings.Contains(r.stderr, "already part of this config") {
		t.Errorf("same file: exit %d, stderr %s", r.code, r.stderr)
	}
	if r := invoke(env, "", "layer", "add", "main", env.Cwd); r.code != ExitErr || !strings.Contains(r.stderr, "already part of this config") {
		t.Errorf("the main config: exit %d, stderr %s", r.code, r.stderr)
	}
}

func TestLayerAddResolvesTheStoredPath(t *testing.T) {
	env, home := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	rel, err := filepath.Rel(env.Cwd, work)
	if err != nil {
		t.Skip("no relative path between the temp dirs")
	}

	mustOK(t, env, "layer", "add", "rel", rel)

	reg, err := layers.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(env.Cwd)
	if got := reg.For(root); len(got) != 1 || got[0].Path != filepath.Join(work, "zakwas.yaml") {
		t.Errorf("stored = %v, want the absolute path of the layer's file", got)
	}
}

func TestLayerAddAndRemoveRefuseAgentSessions(t *testing.T) {
	env, home := setup(t, guardedFiles)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "kept", work)
	before, err := os.ReadFile(layers.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	extra := layerRepo(t, "~/.extra", "extra\n")
	env.AgentSession = "Claude Code"

	for _, args := range [][]string{{"add", "extra", extra}, {"remove", "kept"}} {
		r := invoke(env, "", append([]string{"layer"}, args...)...)
		if r.code != ExitErr || !strings.Contains(r.stderr, "refusing to run layer "+args[0]+" from a Claude Code session") || !strings.Contains(r.stderr, "agents.guard: false") {
			t.Errorf("%v: exit %d\nstderr: %s", args, r.code, r.stderr)
		}
	}
	if after, _ := os.ReadFile(layers.Path(home)); string(after) != string(before) {
		t.Errorf("a refused command changed the registry:\n%s", after)
	}
	if r := invoke(env, "", "layer", "list"); r.code != ExitOK || !strings.Contains(r.stdout, "kept") {
		t.Errorf("list from an agent session: exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}

func TestLayerAllowsAgentSessionsWhenTheGuardIsOff(t *testing.T) {
	for name, cfg := range map[string]string{"guard off": "agents:\n  guard: false\n  providers: [claude]\n" + filesOnly, "no agents section": filesOnly} {
		t.Run(name, func(t *testing.T) {
			env, _ := setup(t, cfg)
			env.AgentSession = "Codex"
			mustOK(t, env, "layer", "add", "work", layerRepo(t, "~/.work", "work\n"))
			mustOK(t, env, "layer", "remove", "work")
		})
	}
}

func TestLayerRefusesInsideARun(t *testing.T) {
	env, home := setup(t, filesOnly)
	env.LockHeldBy = "4242"

	r := invoke(env, "", "layer", "add", "work", layerRepo(t, "~/.work", "work\n"))

	if r.code != ExitErr || !strings.Contains(r.stderr, "can't run layer inside a zakwas run") {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
	if exists(layers.Path(home)) {
		t.Error("the registry was written")
	}
}

func TestLayerRegistryDamageFailsPlanning(t *testing.T) {
	env, home := setup(t, filesOnly)
	if err := os.MkdirAll(filepath.Dir(layers.Path(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layers.Path(home), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "plan")

	if r.code != ExitErr || !strings.Contains(r.stderr, "layers.json") {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestPlanJSONAndMCPConfigNameTheLayers(t *testing.T) {
	env, _ := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)

	cfg, err := loadConfig("", env)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.LayerRefs(); len(got) != 1 || got[0].Name != "work" {
		t.Fatalf("layers = %v", got)
	}
	tools := &mcpTools{env: env}
	res, err := tools.config(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Layers []struct{ Name, Path string }
	}
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Layers) != 1 || out.Layers[0].Name != "work" || out.Layers[0].Path != filepath.Join(work, "zakwas.yaml") {
		t.Errorf("zakwas_config layers = %+v", out.Layers)
	}
}

func TestProposeAndApproveLoadTheLayersToo(t *testing.T) {
	env, home := proposeSetup(t)
	layer := writeFiles(t, map[string]string{
		"zakwas.yaml": "templates:\n  files: [{src: note.tmpl, dst: ~/.layernote}]\n",
		"note.tmpl":   "layer of {{ .Home }}\n",
	})
	mustOK(t, env, "layer", "add", "work", layer)
	mustOK(t, env, "apply", "-y")
	note := filepath.Join(home, ".layernote")
	if !exists(note) {
		t.Fatal("the layer's template was not applied")
	}

	p := propose(t, env, "add-permission", "review", "git", "push")

	planned, err := json.Marshal(p.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(planned), ".layernote") {
		t.Errorf("the proposal plans changes to the layer's file, as if the layer were not registered:\n%s", planned)
	}

	env.StdinTTY = true
	if r := invoke(env, "y\n", "approve", p.ID); r.code != ExitOK {
		t.Fatalf("approve: exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if !exists(note) {
		t.Error("approve removed the layer's file")
	}
	mustOK(t, env, "check")
}

func TestProposalWithAMissingLayerFails(t *testing.T) {
	env, _ := proposeSetup(t)
	layer := writeFiles(t, map[string]string{"zakwas.yaml": "commands: [{name: n, check: 'true', run: 'true'}]\n"})
	mustOK(t, env, "layer", "add", "work", layer)
	if err := os.Remove(filepath.Join(layer, "zakwas.yaml")); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "propose", "add-permission", "review", "git", "push")

	if r.code != ExitErr || !strings.Contains(r.stderr, "zakwas layer remove work") {
		t.Errorf("exit %d\nstderr: %s", r.code, r.stderr)
	}
}

func TestRunningFromALayerCheckoutIsRefused(t *testing.T) {
	env, home := setup(t, filesOnly)
	work := layerRepo(t, "~/.work", "work\n")
	mustOK(t, env, "layer", "add", "work", work)
	mustOK(t, env, "apply", "-y")

	for _, args := range [][]string{{"plan"}, {"check"}, {"apply", "-y"}, {"propose", "add-permission", "deny", "rm"}} {
		inLayer := env
		inLayer.Cwd = work
		r := invoke(inLayer, "", args...)
		if r.code != ExitErr || !strings.Contains(r.stderr, `is layer "work" of `) {
			t.Errorf("%v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
		}
	}
	if !exists(filepath.Join(home, ".zshrc")) || !exists(filepath.Join(home, ".work")) {
		t.Error("a refused run removed files")
	}
	nested := layerRepo(t, "~/.nested", "nested\n")
	inLayer := env
	inLayer.Cwd = work
	before, _ := os.ReadFile(layers.Path(home))
	for _, args := range [][]string{{"layer", "add", "nested", nested}, {"layer", "remove", "work"}, {"layer", "list"}} {
		r := invoke(inLayer, "", args...)
		if r.code != ExitErr || !strings.Contains(r.stderr, `is layer "work" of `) {
			t.Errorf("%v from a layer checkout: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
		}
	}
	if after, _ := os.ReadFile(layers.Path(home)); string(after) != string(before) {
		t.Errorf("a refused layer command changed the registry:\n%s", after)
	}
}

func TestApplyRefusesLayersChangedWhileWaitingForTheLock(t *testing.T) {
	env, home := setup(t, filesOnly)
	cfg, err := loadConfig("", env)
	if err != nil {
		t.Fatal(err)
	}
	if err := layersUnchanged(cfg, env); err != nil {
		t.Fatal(err)
	}
	mustOK(t, env, "layer", "add", "work", layerRepo(t, "~/.work", "work\n"))

	err = layersUnchanged(cfg, env)

	if err == nil || !strings.Contains(err.Error(), "registered layers changed") {
		t.Errorf("err = %v", err)
	}
	reloaded, err := loadConfig("", env)
	if err != nil {
		t.Fatal(err)
	}
	if err := layersUnchanged(reloaded, env); err != nil {
		t.Errorf("after a reload: %v", err)
	}
	if exists(filepath.Join(home, ".work")) {
		t.Error("the check applied something")
	}
}
