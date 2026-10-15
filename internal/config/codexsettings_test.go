package config

import "testing"

func TestCodexSettingsValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		providers []string
		settings  map[string]any
		fail      bool
	}{
		{"valid", []string{ProviderCodex}, map[string]any{"agents": map[string]any{"enabled": true, "max_concurrent_threads_per_session": 8}}, false},
		{"opt-in", nil, map[string]any{"model": "x"}, true},
		{"null", []string{ProviderCodex}, map[string]any{"model": nil}, true},
		{"reserved", []string{ProviderCodex}, map[string]any{"mcp_servers": map[string]any{"x": true}}, true},
		{"empty key", []string{ProviderCodex}, map[string]any{"": true}, true},
		{"empty map", []string{ProviderCodex}, map[string]any{"agents": map[string]any{}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := Agents{Providers: tt.providers, Codex: &CodexSettings{Settings: tt.settings}}
			if errs := a.validateCodexSettings(); (len(errs) != 0) != tt.fail {
				t.Fatalf("errors = %v", errs)
			}
		})
	}
}
