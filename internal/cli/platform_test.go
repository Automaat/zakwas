package cli

import (
	"strings"
	"testing"
)

func TestWindowsDropsUnconfiguredMacModules(t *testing.T) {
	env, _ := setup(t, "protect: {immutable: false}\n")
	env.GOOS = "windows"
	cfg, err := loadConfig("", env.Cwd, env.Home)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, m := range Modules(cfg, env) {
		names = append(names, m.Name())
	}

	if got, want := strings.Join(names, ","), "files,links,templates"; got != want {
		t.Errorf("modules on Windows = %s, want %s", got, want)
	}
}

func TestWindowsFailsConfiguredMacModulesWithAMessage(t *testing.T) {
	tests := []struct {
		name   string
		cfg    string
		module string
		want   string
	}{
		{"brew", "brew: {file: Brewfile}\n", "brew", "macOS only"},
		{"mise", "mise: {config: m.toml}\n", "mise", "not supported on Windows yet"},
		{"commands", "commands: [{name: n, check: 'true', run: 'true'}]\n", "commands", "not supported on Windows yet"},
		{"defaults", "defaults: [{domain: a, key: k, value: 1}]\n", "defaults", "macOS only"},
		{"system", "system: {sudoTouchID: true}\n", "system", "macOS only"},
		{"launchd", "launchd: [{label: a.b, program: [/bin/true], schedule: {every: 1h}}]\n", "launchd", "macOS only"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _ := setup(t, tt.cfg)
			env.GOOS = "windows"

			r := invoke(env, "", "plan", "--only", tt.module)

			if r.code != ExitErr {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, ExitErr, r.stdout, r.stderr)
			}
			if out := r.stdout + r.stderr; !strings.Contains(out, tt.module+": ") || !strings.Contains(out, tt.want) || !strings.Contains(out, "Windows") {
				t.Errorf("output lacks a clear message for %s:\nstdout: %s\nstderr: %s", tt.module, r.stdout, r.stderr)
			}
		})
	}
}

func TestWindowsKeepsTheAgentAndFileModules(t *testing.T) {
	env, _ := setup(t, "agents: {providers: [claude]}\nfiles: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n")
	env.GOOS = "windows"

	r := invoke(env, "", "plan", "--only", "files,agents")

	if r.code != ExitOK || !strings.Contains(r.stdout, "+ ~/.zshrc") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
}
