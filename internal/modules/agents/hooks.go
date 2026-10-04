package agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

type klaudiushState struct {
	ConfigHash  string `json:"configHash"`
	TargetsHash string `json:"targetsHash"`
	BinaryPath  string `json:"binaryPath"`
	BinarySize  int64  `json:"binarySize"`
	BinaryTime  int64  `json:"binaryTime"`
}

type klaudiushProvider struct {
	Enabled      *bool  `toml:"enabled"`
	Experimental bool   `toml:"experimental"`
	HooksPath    string `toml:"hooks_config_path"`
	SettingsPath string `toml:"settings_path"`
	PluginPath   string `toml:"plugin_path"`
}

func (p klaudiushProvider) enabled(defaultValue bool) bool {
	if p.Enabled == nil {
		return defaultValue
	}
	return *p.Enabled
}

type klaudiushConfig struct {
	Providers struct {
		Claude   klaudiushProvider `toml:"claude"`
		Codex    klaudiushProvider `toml:"codex"`
		Gemini   klaudiushProvider `toml:"gemini"`
		Opencode klaudiushProvider `toml:"opencode"`
	} `toml:"providers"`
}

func (m *Module) klaudiushConfigPath() string {
	base := filepath.Join(m.Paths.Home, ".config")
	if m.Paths.Home == os.Getenv("HOME") {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
			base = xdg
		}
	}
	return filepath.Join(base, "klaudiush", "config.toml")
}

func (m *Module) klaudiushBinary() (string, error) {
	if m.klaudiushPath != "" {
		return m.klaudiushPath, nil
	}
	return exec.LookPath("klaudiush")
}

func (m *Module) hookPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(m.Paths.Home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return filepath.Join("/", path)
	}
	return path
}

func (m *Module) planKlaudiush() (*engine.Change, error) {
	configPath := m.klaudiushConfigPath()
	data, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return m.klaudiushChange("config pending; register enabled providers on apply"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("klaudiush config: %w", err)
	}
	var cfg klaudiushConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("klaudiush config %s: %w", configPath, err)
	}
	if p := cfg.Providers.Codex; p.enabled(false) && (!p.Experimental || p.HooksPath == "") {
		return nil, errors.New("klaudiush codex: enabled provider needs experimental = true and hooks_config_path")
	}
	if p := cfg.Providers.Gemini; p.enabled(false) && p.SettingsPath == "" {
		return nil, errors.New("klaudiush gemini: enabled provider needs settings_path")
	}
	if !m.Runner.Installed("klaudiush") {
		return m.klaudiushChange("register enabled providers after klaudiush is installed"), nil
	}
	binary, err := m.klaudiushBinary()
	if err != nil {
		return nil, fmt.Errorf("klaudiush binary: %w", err)
	}
	var missing []string
	check := func(provider, path string, events []string, command func(string) string) error {
		ok, err := hooksRegistered(m.hookPath(path), events, command)
		if err != nil {
			return fmt.Errorf("klaudiush %s hooks: %w", provider, err)
		}
		if !ok {
			missing = append(missing, provider)
		}
		return nil
	}
	if cfg.Providers.Claude.enabled(true) {
		path := filepath.Join(m.Paths.Home, ".claude", "settings.json")
		if err := check("claude", path, []string{"PreToolUse", "PostToolUse"}, func(event string) string { return binary + " --hook-type " + event }); err != nil {
			return nil, err
		}
	}
	if p := cfg.Providers.Codex; p.enabled(false) && p.Experimental && p.HooksPath != "" {
		if err := check("codex", p.HooksPath, []string{"SessionStart", "AfterToolUse", "Stop"}, func(event string) string { return binary + " --provider codex --event " + event }); err != nil {
			return nil, err
		}
	}
	if p := cfg.Providers.Gemini; p.enabled(false) && p.SettingsPath != "" {
		if err := check("gemini", p.SettingsPath, []string{"BeforeTool", "AfterTool", "SessionStart", "SessionEnd", "Notification", "PreCompress"}, func(event string) string { return binary + " --provider gemini --event " + event }); err != nil {
			return nil, err
		}
	}
	if p := cfg.Providers.Opencode; p.enabled(false) {
		ok, err := opencodeHookRegistered(m.opencodeKlaudiushPath(p), binary)
		if err != nil {
			return nil, fmt.Errorf("klaudiush opencode hooks: %w", err)
		}
		if !ok {
			missing = append(missing, "opencode")
		}
	}
	if len(missing) == 0 {
		current, err := m.captureKlaudiushState()
		if err != nil {
			return nil, err
		}
		var previous klaudiushState
		if err := loadState(m.klaudiushStatePath(), &previous); err != nil {
			return nil, err
		}
		if current != previous {
			return m.klaudiushChange("refresh registration after klaudiush or its config changed"), nil
		}
		return nil, nil
	}
	return m.klaudiushChange("register " + strings.Join(missing, ", ")), nil
}

func (m *Module) klaudiushStatePath() string {
	return filepath.Join(m.Paths.Home, ".local", "state", "zakwas", "klaudiush.json")
}

func (m *Module) captureKlaudiushState() (klaudiushState, error) {
	data, err := os.ReadFile(m.klaudiushConfigPath())
	if err != nil {
		return klaudiushState{}, fmt.Errorf("klaudiush config: %w", err)
	}
	var cfg klaudiushConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return klaudiushState{}, fmt.Errorf("klaudiush config: %w", err)
	}
	var targets []byte
	for _, path := range m.klaudiushTargetPaths(cfg) {
		content, err := os.ReadFile(path)
		if err != nil {
			return klaudiushState{}, fmt.Errorf("klaudiush hooks %s: %w", path, err)
		}
		targets = append(targets, path...)
		targets = append(targets, 0)
		targets = append(targets, content...)
		targets = append(targets, 0)
	}
	binary, err := m.klaudiushBinary()
	if err != nil {
		return klaudiushState{}, fmt.Errorf("klaudiush binary: %w", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return klaudiushState{}, fmt.Errorf("klaudiush binary %s: %w", binary, err)
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return klaudiushState{}, fmt.Errorf("klaudiush binary %s: %w", binary, err)
	}
	return klaudiushState{
		ConfigHash:  fmt.Sprintf("%x", sha256.Sum256(data)),
		TargetsHash: fmt.Sprintf("%x", sha256.Sum256(targets)),
		BinaryPath:  resolved,
		BinarySize:  info.Size(),
		BinaryTime:  info.ModTime().UnixNano(),
	}, nil
}

func (m *Module) opencodeKlaudiushPath(p klaudiushProvider) string {
	if p.PluginPath != "" {
		return m.hookPath(p.PluginPath)
	}
	base := filepath.Join(m.Paths.Home, ".config")
	if m.Paths.Home == os.Getenv("HOME") {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
			base = xdg
		}
	}
	return filepath.Join(base, "opencode", "plugin", "klaudiush.ts")
}

func (m *Module) klaudiushTargetPaths(cfg klaudiushConfig) []string {
	var paths []string
	if cfg.Providers.Claude.enabled(true) {
		paths = append(paths, filepath.Join(m.Paths.Home, ".claude", "settings.json"))
	}
	if p := cfg.Providers.Codex; p.enabled(false) && p.Experimental && p.HooksPath != "" {
		paths = append(paths, m.hookPath(p.HooksPath))
	}
	if p := cfg.Providers.Gemini; p.enabled(false) && p.SettingsPath != "" {
		paths = append(paths, m.hookPath(p.SettingsPath))
	}
	if p := cfg.Providers.Opencode; p.enabled(false) {
		paths = append(paths, m.opencodeKlaudiushPath(p))
	}
	return paths
}

func (m *Module) klaudiushChange(detail string) *engine.Change {
	cmd := runner.Cmd{Name: "klaudiush", Args: []string{"init", "--install-hooks", "--global"}, Dir: "/", Stream: true}
	return &engine.Change{
		Action: engine.Run, Target: "klaudiush hooks", Detail: detail,
		Diff: cmd.String(), Streams: true, Group: "klaudiush",
		Apply: func(ctx context.Context) error {
			if err := runner.Check(ctx, m.Runner, cmd); err != nil {
				return err
			}
			state, err := m.captureKlaudiushState()
			if err != nil {
				return err
			}
			if err := saveState(m.klaudiushStatePath(), state); err != nil {
				return err
			}
			remaining, err := m.planKlaudiush()
			if err != nil {
				return err
			}
			if remaining != nil {
				return fmt.Errorf("klaudiush did not converge: %s", remaining.Detail)
			}
			return nil
		},
	}
}

func hooksRegistered(path string, events []string, command func(string) string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	for _, event := range events {
		found := false
		for _, group := range settings.Hooks[event] {
			for _, hook := range group.Hooks {
				if hook.Type == "command" && hook.Command == command(event) {
					found = true
				}
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func opencodeHookRegistered(path, binary string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	literal, err := marshal(binary)
	if err != nil {
		return false, err
	}
	source := string(data)
	return strings.Contains(source, string(literal)) &&
		strings.Contains(source, `"tool.execute.before":`) &&
		strings.Contains(source, `case "session.idle":`), nil
}
