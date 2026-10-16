package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func moduleNames(mods []engine.Module) []string {
	var names []string
	for _, m := range mods {
		names = append(names, m.Name())
	}
	return names
}

func TestModulesBuiltPerPlatform(t *testing.T) {
	cfg := &config.Config{
		Root:     t.TempDir(),
		Brew:     &config.Brew{File: "Brewfile"},
		Mise:     &config.Mise{Config: "m.toml"},
		Defaults: []config.Default{{Domain: "d", Key: "k", Value: true}},
		Launchd:  []config.LaunchdJob{{Label: "dev.example.job"}},
		Agents:   &config.Agents{},
	}
	tests := []struct {
		goos string
		want []string
	}{
		{platform.Darwin, []string{"system", "files", "links", "templates", "brew", "mise", "agents", "commands", "launchd", "defaults"}},
		{platform.Linux, []string{"system", "files", "links", "templates", "agents", "commands"}},
		{platform.Windows, []string{"files", "templates", "agents"}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			env := Env{Home: t.TempDir(), Runner: runnertest.New(), GOOS: tt.goos}
			got := moduleNames(Modules(cfg, env))
			if !slices.Equal(got, tt.want) {
				t.Errorf("modules = %v, want %v", got, tt.want)
			}
			for _, name := range got {
				if !platform.Supported(name, tt.goos) {
					t.Errorf("built %s, which %s does not support", name, tt.goos)
				}
			}
		})
	}
}

func TestLaunchdStateDoesNotBuildTheModuleOnLinux(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state", "zakwas", "launchd.json")
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Root: t.TempDir()}
	if got := moduleNames(Modules(cfg, Env{Home: home, Runner: runnertest.New(), GOOS: platform.Linux})); slices.Contains(got, "launchd") {
		t.Errorf("launchd built on linux: %v", got)
	}
	if got := moduleNames(Modules(cfg, Env{Home: home, Runner: runnertest.New(), GOOS: platform.Darwin})); !slices.Contains(got, "launchd") {
		t.Errorf("launchd not built on darwin: %v", got)
	}
}

func linuxSetup(t *testing.T, cfg string) Env {
	t.Helper()
	env, _ := setup(t, cfg)
	env.GOOS = platform.Linux
	return env
}

func TestLinuxRejectsMacOnlyConfig(t *testing.T) {
	cfg := "links: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\nbrew: {file: Brewfile}\nsystem: {sudoTouchID: true}\n"
	for _, cmd := range []string{"plan", "check", "apply"} {
		t.Run(cmd, func(t *testing.T) {
			env := linuxSetup(t, cfg)
			args := []string{cmd}
			if cmd == "apply" {
				args = append(args, "-y")
			}
			r := invoke(env, "", args...)
			if r.code != ExitErr {
				t.Fatalf("exit %d, want %d\n%+v", r.code, ExitErr, r)
			}
			for _, want := range []string{"brew: not supported on Linux", "system.sudoTouchID: not supported on Linux", platform.DocsAnchor} {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
				}
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want nothing", r.stdout)
			}
		})
	}
}

func TestLinuxOnlyNamesAnUnsupportedModule(t *testing.T) {
	env := linuxSetup(t, linksOnly)
	r := invoke(env, "", "plan", "--only", "brew")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "brew: not supported on Linux") || strings.Contains(r.stderr, "unconfigured") {
		t.Errorf("%+v", r)
	}
}

func TestLinuxImmutableWarnsOnceAndStaysClean(t *testing.T) {
	env := linuxSetup(t, "protect: {immutable: true}\n"+linksOnly+"files: [{src: dotfiles/zshrc, dst: ~/.config/rc}]\n")
	plan := invoke(env, "", "plan")
	if plan.code != ExitOK || strings.Count(plan.stdout, "protect.immutable is macOS-only; files are read-only copies") != 1 {
		t.Fatalf("%+v", plan)
	}
	if strings.Contains(plan.stdout, "set immutable") {
		t.Errorf("plan sets immutable:\n%s", plan.stdout)
	}
	if r := invoke(env, "", "apply", "-y"); r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	t.Cleanup(func() { _ = install.Unlock(env.Home) })
	info, err := os.Stat(filepath.Join(env.Home, ".config", "rc"))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Errorf("installed file: %v, %v", info, err)
	}
	check := invoke(env, "", "check")
	if check.code != ExitOK || strings.Count(check.stdout, "protect.immutable is macOS-only") != 1 {
		t.Errorf("check: %+v", check)
	}
}

func TestDarwinImmutableDoesNotWarn(t *testing.T) {
	env, _ := setup(t, "protect: {immutable: true}\n"+linksOnly)
	if r := invoke(env, "", "plan"); strings.Contains(r.stdout, "macOS-only") {
		t.Errorf("darwin warned:\n%s", r.stdout)
	}
}

func TestLinuxOfflineCheckListsOnlyWhatLinuxSkips(t *testing.T) {
	env := linuxSetup(t, linksOnly)
	r := invoke(env, "", "check", "--offline")
	if !strings.Contains(r.stdout, "offline: not checked: system, agents, commands;") {
		t.Errorf("%+v", r)
	}
	for _, name := range platform.UnsupportedModules(platform.Linux) {
		if strings.Contains(r.stdout, name) {
			t.Errorf("offline note mentions %s:\n%s", name, r.stdout)
		}
	}
}

func TestInitOnLinuxSkipsBrewAndMise(t *testing.T) {
	home := initHome(t)
	repo := filepath.Join(t.TempDir(), "repo")
	fake := runnertest.New().OnOK("git init -q -b main", "")
	r := invoke(Env{Home: home, Cwd: home, Runner: fake, GOOS: platform.Linux}, "", "init", repo, "--add", "~/.zshrc")
	if r.code != ExitOK {
		t.Fatalf("%+v", r)
	}
	cfg := read(t, filepath.Join(repo, "zakwas.yaml"))
	for _, absent := range []string{"brew:", "mise:", "protect:"} {
		if strings.Contains(cfg, absent) {
			t.Errorf("zakwas.yaml has %q:\n%s", absent, cfg)
		}
	}
	for _, want := range []string{"no brew section: the brew module is not supported on Linux", "no mise section: the mise module is not supported on Linux", "On a new machine:"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("summary lacks %q:\n%s", want, r.stdout)
		}
	}
	for _, call := range fake.Lines() {
		if strings.HasPrefix(call, "brew ") || strings.HasPrefix(call, "mise ") {
			t.Errorf("init ran %q", call)
		}
	}
	if _, err := config.LoadFor(filepath.Join(repo, "zakwas.yaml"), home, platform.Linux); err != nil {
		t.Errorf("generated config does not load on Linux: %v", err)
	}
}

func TestAdvertisedSurfaceFollowsThePlatform(t *testing.T) {
	macOnly := []string{"Homebrew", "brew", "add-brew", "pin-mise", "cask", "Mac"}
	tests := []struct {
		goos      string
		mentions  bool
		kindCount int
	}{
		{platform.Darwin, true, 7},
		{platform.Linux, false, 5},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := len(proposal.KindsFor(tt.goos)); got != tt.kindCount {
				t.Errorf("%d operations, want %d", got, tt.kindCount)
			}
			mcpTools := &mcpTools{env: Env{GOOS: tt.goos}}
			var surface []string
			surface = append(surface, usage(tt.goos), commandHelp("init", tt.goos), commandHelp("upgrade", tt.goos), initUsage(tt.goos), proposeUsage(tt.goos), mcpInstructions(tt.goos))
			surface = append(surface, strings.Join(offlineSkippedOn(tt.goos), ","))
			for _, tool := range mcpTools.tools() {
				surface = append(surface, tool.Title, tool.Description)
				if tool.Name == "zakwas_propose" {
					schema, err := json.Marshal(tool.Schema)
					if err != nil {
						t.Fatal(err)
					}
					surface = append(surface, string(schema))
				}
			}
			text := strings.Join(surface, "\n")
			for _, word := range macOnly {
				if got := strings.Contains(text, word); got != tt.mentions {
					i := strings.Index(text, word)
					t.Errorf("surface mentions %q = %v, want %v: ...%s...", word, got, tt.mentions, text[max(0, i-60):min(len(text), i+60)])
				}
			}
		})
	}
}

func TestLinuxProposeRefusesMacOnlyOperations(t *testing.T) {
	for _, args := range [][]string{{"add-brew", "jq"}, {"pin-mise", "jq", "1.8.0"}} {
		env := linuxSetup(t, linksOnly)
		r := invoke(env, "", append([]string{"propose"}, args...)...)
		if r.code == ExitOK || !strings.Contains(r.stderr, args[0]+": ") || !strings.Contains(r.stderr, "not supported on Linux") {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

func TestPrintDefaultsExceptHidesOneFlag(t *testing.T) {
	var buf bytes.Buffer
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(&buf)
	fs.Bool("cask", false, "add-brew: a cask")
	fs.String("providers", "x", "who")
	if err := fs.Parse([]string{"-providers", "y"}); err != nil {
		t.Fatal(err)
	}
	printDefaultsExcept(fs, "cask")
	got := buf.String()
	if strings.Contains(got, "cask") || !strings.Contains(got, "-providers string") || !strings.Contains(got, `(default "x")`) {
		t.Errorf("output:\n%s", got)
	}
	var all bytes.Buffer
	fs.SetOutput(&all)
	printDefaultsExcept(fs, "")
	var want bytes.Buffer
	fs.SetOutput(&want)
	fs.PrintDefaults()
	if all.String() != want.String() {
		t.Errorf("nothing hidden differs from PrintDefaults:\n%s\nwant\n%s", all.String(), want.String())
	}
}

func TestCompletionOffersCaskOnlyWhereBrewExists(t *testing.T) {
	want := platform.Supported("brew", platform.Current())
	for _, shell := range shells {
		if got := strings.Contains(completion(t, shell), "cask"); got != want {
			t.Errorf("%s completion mentions cask = %v, want %v", shell, got, want)
		}
	}
}

func TestWindowsRejectsWhatItCannotRun(t *testing.T) {
	tests := []struct {
		name, cfg, want string
	}{
		{"brew", "brew: {file: Brewfile}\n", "brew: not supported on Windows"},
		{"mise", "mise: {config: m.toml}\n", "mise: not supported on Windows"},
		{"commands", "commands: [{name: n, check: 'true', run: 'true'}]\n", "commands: not supported on Windows (its checks and steps run with sh"},
		{"links", "links: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n", "links: not supported on Windows"},
		{"system dirs", "system: {dirs: [{path: ~/.ssh}]}\n", "system: not supported on Windows"},
		{"skills", "agents: {skills: [{path: skills/mine}]}\n", "agents.skills: not supported on Windows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, tt.cfg)
			env.GOOS = platform.Windows

			r := invoke(env, "", "plan")

			if r.code != ExitErr || !strings.Contains(r.stderr, tt.want) || !strings.Contains(r.stderr, platform.DocsAnchor) {
				t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
		})
	}
}

func TestWindowsPlansTheAgentAndFileModules(t *testing.T) {
	env, _ := setup(t, "agents: {providers: [claude]}\nfiles: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n")
	env.GOOS = platform.Windows

	r := invoke(env, "", "plan", "--only", "files,agents")

	if r.code != ExitOK || !strings.Contains(r.stdout, "+ ~/.zshrc") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}
