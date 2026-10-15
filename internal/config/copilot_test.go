package config

import (
	"slices"
	"strings"
	"testing"
)

func TestCopilotIsOptIn(t *testing.T) {
	for _, tt := range []struct {
		name, yaml string
		want       []string
	}{
		{"default providers", "agents:\n  mcp:\n    a: {command: a}\n", nil},
		{"named in providers", "agents:\n  providers: [claude, copilot]\n  mcp:\n    a: {command: a}\n", []string{"a"}},
		{"listed by one entry", "agents:\n  mcp:\n    a: {command: a, providers: [copilot]}\n    b: {command: b}\n", []string{"a"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(writeConfig(t, tt.yaml), "/h")
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range c.Agents.MCPFor(ProviderCopilot) {
				got = append(got, s.Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("copilot servers = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCopilotProfileBlock(t *testing.T) {
	const profile = "  profiles:\n    review:\n      description: Reviews\n      copilot: {model: gpt-5, overrides: {%s: x}}\n"
	for _, tt := range []struct {
		name, yaml, want string
	}{
		{"not named", "agents:\n  providers: [claude]\n" + strings.Replace(profile, "%s", "infer", 1), "copilot: the profile is not rendered for copilot"},
		{"controlled key", "agents:\n  providers: [copilot]\n" + strings.Replace(profile, "%s", "tools", 1), "copilot.overrides.tools: zakwas sets it from the profile's own fields"},
		{"default providers", "agents:\n" + strings.Replace(profile, "%s", "infer", 1), "Copilot is opt-in, so name it in agents.providers"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.yaml), "/h")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := Load(writeConfig(t, "agents:\n  providers: [copilot]\n"+strings.Replace(profile, "%s", "infer", 1)), "/h"); err != nil {
		t.Errorf("named copilot: %v", err)
	}
}
