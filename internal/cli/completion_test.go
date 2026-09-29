package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func completion(t *testing.T, shell string) string {
	t.Helper()
	r := invoke(Env{Runner: runnertest.New()}, "", "completion", shell)
	if r.code != ExitOK {
		t.Fatalf("completion %s: exit %d: %s", shell, r.code, r.stderr)
	}
	return r.stdout
}

func TestCompletionCoversEverything(t *testing.T) {
	m := newCompletionModel()
	for _, shell := range shells {
		script := completion(t, shell)
		for _, c := range commandList {
			if !strings.Contains(script, c.name) {
				t.Errorf("%s: no command %q", shell, c.name)
			}
		}
		for _, f := range m.all() {
			if !strings.Contains(script, f.spelling()) && !strings.Contains(script, "-l "+f.name) && !strings.Contains(script, "-s "+f.name) {
				t.Errorf("%s: no flag %q", shell, f.name)
			}
		}
		for _, mod := range ModuleNames {
			if !strings.Contains(script, mod) {
				t.Errorf("%s: no module %q", shell, mod)
			}
		}
	}
}

func TestCompletionModulesMatchCLI(t *testing.T) {
	env, _ := setup(t, "brew: {file: Brewfile}\nmise: {config: m.toml}\n")
	cfg, err := loadConfig("", env.Cwd, env.Home)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range Modules(cfg, env) {
		names = append(names, m.Name())
	}
	if strings.Join(names, ",") != strings.Join(ModuleNames, ",") {
		t.Errorf("modules %v, completion offers %v", names, ModuleNames)
	}
}

func TestCompletionUsage(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "tcsh"}, {"completion", "zsh", "bash"}} {
		if r := invoke(Env{Runner: runnertest.New()}, "", args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, r.code, ExitUsage)
		}
	}
}

func TestCompletionScriptsParse(t *testing.T) {
	for shell, check := range map[string][]string{"zsh": {"-n"}, "bash": {"-n"}, "fish": {"--no-execute"}} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s not installed", shell)
			}
			file := filepath.Join(t.TempDir(), "zakwas."+shell)
			if err := os.WriteFile(file, []byte(completion(t, shell)), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, append(check, file)...).CombinedOutput(); err != nil {
				t.Errorf("%s rejects the script: %v\n%s", shell, err, out)
			}
		})
	}
}

func TestBashCompletion(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	tests := []struct {
		words []string
		want  string
	}{
		{[]string{"zakwas", ""}, strings.Join(commandNames(), " ")},
		{[]string{"zakwas", "ap"}, "apply"},
		{[]string{"zakwas", "-y", "ap"}, "apply"},
		{[]string{"zakwas", "-c", "x.yaml", "pl"}, "plan"},
		{[]string{"zakwas", "plan", "--d"}, "--diff"},
		{[]string{"zakwas", "--only", "b"}, "brew"},
		{[]string{"zakwas", "--only", "brew,m"}, "brew,mise"},
		{[]string{"zakwas", "check", "-only", "files,links,t"}, "files,links,templates"},
		{[]string{"zakwas", "init", "dir", "--a"}, "--add"},
		{[]string{"zakwas", "self-update", "-"}, "--version"},
		{[]string{"zakwas", "completion", "z"}, "zsh"},
	}
	script := completion(t, "bash")
	for _, tt := range tests {
		t.Run(strings.Join(tt.words, " "), func(t *testing.T) {
			var quoted []string
			for _, w := range tt.words {
				quoted = append(quoted, "'"+w+"'")
			}
			prog := script + "\nCOMP_WORDS=(" + strings.Join(quoted, " ") + ")\nCOMP_CWORD=" + string(rune('0'+len(tt.words)-1)) + "\n_zakwas\necho \"${COMPREPLY[*]}\"\n"
			out, err := exec.Command(bash, "-c", prog).CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompletionHelp(t *testing.T) {
	for _, args := range [][]string{{"completion", "--help"}, {"completion", "-h"}, {"completion", "zsh", "--help"}} {
		r := invoke(Env{Runner: runnertest.New()}, "", args...)
		if r.code != ExitOK || !strings.Contains(r.stdout, "Usage: zakwas completion") || r.stderr != "" {
			t.Errorf("%v: %+v", args, r)
		}
	}
}
