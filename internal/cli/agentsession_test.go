package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectAgentSession(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"a terminal", nil, ""},
		{"unrelated variables", map[string]string{"TERM": "xterm", "CLAUDE_CONFIG_DIR": "/x", "OPENCODE_CONFIG_DIR": "/y"}, ""},
		{"empty marker", map[string]string{"CLAUDECODE": ""}, ""},
		{"claude", map[string]string{"CLAUDECODE": "1"}, "Claude Code"},
		{"claude entrypoint", map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, "Claude Code"},
		{"codex sandbox", map[string]string{"CODEX_SANDBOX": "seatbelt"}, "Codex"},
		{"codex thread", map[string]string{"CODEX_THREAD_ID": "abc"}, "Codex"},
		{"opencode", map[string]string{"OPENCODE": "1"}, "opencode"},
		{"copilot", map[string]string{"COPILOT_AGENT_SESSION_ID": "abc"}, "Copilot CLI"},
		{"gemini", map[string]string{"GEMINI_CLI": "1"}, "Gemini CLI"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectAgentSession(func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("session = %q, want %q", got, tt.want)
			}
		})
	}
}

const guardedLinks = "agents:\n  providers: [opencode]\n" + linksOnly

func TestGuardRefusesApplyAndUpgradeFromAnAgent(t *testing.T) {
	for _, cmd := range []string{"apply", "upgrade"} {
		t.Run(cmd, func(t *testing.T) {
			env, home := setup(t, guardedLinks)
			env.AgentSession, env.StdinTTY = "Claude Code", true
			r := invoke(env, "y\n", cmd, "-y")
			if r.code != ExitErr {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, ExitErr, r.stdout, r.stderr)
			}
			for _, want := range []string{"refusing to " + cmd, "Claude Code", "zakwas plan", "agents.guard: false"} {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr lacks %q: %s", want, r.stderr)
				}
			}
			if _, err := os.Lstat(filepath.Join(home, ".zshrc")); err == nil {
				t.Error("the refused run changed the system")
			}
		})
	}
}

func TestGuardLetsAgentsPlanAndCheck(t *testing.T) {
	for cmd, want := range map[string]int{"plan": ExitOK, "check": ExitDrift} {
		env, _ := setup(t, guardedLinks)
		env.AgentSession = "Codex"
		if r := invoke(env, "", cmd); r.code != want || !strings.Contains(r.stdout, "+ ~/.zshrc") {
			t.Errorf("%s: exit %d, want %d\nstdout: %s\nstderr: %s", cmd, r.code, want, r.stdout, r.stderr)
		}
	}
}

func TestGuardAllowsApplyWhenOffOrWithoutAnAgentsSection(t *testing.T) {
	tests := []struct{ name, cfg, session string }{
		{"guard off", "agents:\n  guard: false\n  providers: [opencode]\n" + linksOnly, "Claude Code"},
		{"no agents section", linksOnly, "Claude Code"},
		{"a person's terminal", guardedLinks, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, home := setup(t, tt.cfg)
			env.AgentSession = tt.session
			if r := invoke(env, "", "apply", "-y"); r.code != ExitOK {
				t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
			if _, err := os.Lstat(filepath.Join(home, ".zshrc")); err != nil {
				t.Error(err)
			}
		})
	}
}
