package cli

// agentMarkers are environment variables the coding agents set in the
// shells they start, in the order they are checked. An agent can unset
// them, so this stops accidents and prompt injections that don't know of
// it; the agents' own deny rules are the boundary.
var agentMarkers = []struct{ env, agent string }{
	{"CLAUDECODE", "Claude Code"},
	{"CLAUDE_CODE_ENTRYPOINT", "Claude Code"},
	{"CODEX_SANDBOX", "Codex"},
	{"CODEX_SANDBOX_NETWORK_DISABLED", "Codex"},
	{"CODEX_THREAD_ID", "Codex"},
	{"OPENCODE", "opencode"},
	{"COPILOT_AGENT_SESSION_ID", "Copilot CLI"},
	{"GEMINI_CLI", "Gemini CLI"},
}

// DetectAgentSession names the coding agent whose shell this process runs
// in, or returns "" when it looks like a person's terminal.
func DetectAgentSession(getenv func(string) string) string {
	for _, m := range agentMarkers {
		if getenv(m.env) != "" {
			return m.agent
		}
	}
	return ""
}
