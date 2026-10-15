package config

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// CodexSettings declares selected global values using Codex's TOML names.
type CodexSettings struct {
	Settings map[string]any `yaml:"settings" jsonschema_description:"Selected global config.toml values using native Codex names. Supports strings, booleans, finite numbers, arrays and nested mappings. Only declared values are updated; omitted values remain. No templates or secret expansion. Profiles, MCP servers, plugins and marketplaces use their dedicated agents fields."`
}

func (a *Agents) validateCodexSettings() []error {
	if a.Codex == nil || len(a.Codex.Settings) == 0 {
		return nil
	}
	var errs []error
	if !a.Names(ProviderCodex) {
		errs = append(errs, errors.New("agents.codex.settings: explicitly name codex in agents.providers or a provider entry"))
	}
	for key, value := range a.Codex.Settings {
		switch key {
		case "profiles", "mcp_servers", "plugins", "marketplaces":
			errs = append(errs, fmt.Errorf("agents.codex.settings.%s: use the dedicated agents field", key))
		}
		if err := validateCodexValue("agents.codex.settings."+key, key, value); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func validateCodexValue(path, key string, value any) error {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\x00\r\n") {
		return fmt.Errorf("%s: invalid empty or multiline key", path)
	}
	switch v := value.(type) {
	case string, bool, int, int64, uint64:
		return nil
	case float64:
		if !math.IsInf(v, 0) && !math.IsNaN(v) {
			return nil
		}
	case []any:
		for i, e := range v {
			if _, mapping := e.(map[string]any); mapping {
				return fmt.Errorf("%s[%d]: arrays of mappings are not supported", path, i)
			}
			if err := validateCodexValue(fmt.Sprintf("%s[%d]", path, i), key, e); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(v) == 0 {
			return fmt.Errorf("%s: mapping must declare at least one value", path)
		}
		for k, e := range v {
			if err := validateCodexValue(path+"."+k, k, e); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%s: expected a string, boolean, finite number, array or mapping", path)
}
