package cli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/runner"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

const reviewConfig = `[settings]
experimental = true

[tools]
nodejs = "22"   # lts
golang = "latest"
"pipx:black" = { version = "latest", uvx = false }
python = [
  "3.12", # main
  "3.11",
]
jq = "1.7.1"
tools.ruff = "0.6"

[tools."aqua:cli/cli"]
version = "2"
os = ["macos"]

[[watch_files]]
patterns = ["*.toml"]
run = "echo"

[env]
NOTE = """
[tools]
version = '1'
"""
`

func tool(requested, version string, installed bool) miseTool {
	return miseTool{Version: version, RequestedVersion: requested, Installed: installed}
}

func TestWriteMiseConfig(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".config/mise/config.toml")
	if err := writeFile(global, []byte(reviewConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := map[string][]miseTool{
		"node":         {tool("22", "22.21.1", true)},
		"go":           {tool("latest", "1.27.1", true)},
		"pipx:black":   {tool("latest", "24.1.0", false)},
		"python":       {tool("3.11", "3.11.9", true), tool("3.12", "3.12.4", true)},
		"deno":         {tool("1", "1.46.0", true), tool("2", "2.1.0", true)},
		"jq":           {tool("1.7.1", "1.7.1", true)},
		"ruff":         {tool("0.6", "0.6.9", true)},
		"aqua:cli/cli": {tool("2", "2.1.0", true)},
	}
	for name := range tools {
		for i := range tools[name] {
			tools[name][i].Source.Path = global
		}
	}
	fake := runnertest.New().
		OnOK("mise use --global --pin --quiet aqua:cli/cli@2.1.0", "").
		OnOK("mise use --global --pin --quiet go@1.27.1", "").
		OnOK("mise use --global --pin --quiet node@22.21.1", "").
		OnOK("mise use --global --pin --quiet python@3.12.4 python@3.11.9", "").
		On("mise use --global --pin --quiet ruff@0.6.9", runner.Result{ExitCode: 1, Stderr: "mise ERROR cannot edit\nmore"})
	dst := filepath.Join(t.TempDir(), "dotfiles/mise/config.toml")

	unpinned, err := writeMiseConfig(context.Background(), fake, tools, filepath.Join(t.TempDir(), "unused"), dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, dst); got != reviewConfig {
		t.Errorf("copy differs from the global config:\n%s", got)
	}
	want := []string{"deno (several versions in an order init can't read)", "pipx:black (not installed)", "ruff (mise use --global --pin --quiet ruff@0.6.9: exit 1: mise ERROR cannot edit)"}
	if !slices.Equal(unpinned, want) {
		t.Errorf("unpinned = %q, want %q", unpinned, want)
	}
	for _, c := range fake.Calls {
		if c.Dir != "/" {
			t.Errorf("%s ran in %q, want /", c, c.Dir)
		}
		if !slices.Contains(c.Env, "MISE_GLOBAL_CONFIG_FILE="+dst) {
			t.Errorf("%s env %v doesn't point mise at the copy", c, c.Env)
		}
		for _, e := range c.Env {
			if strings.Contains(e, home) {
				t.Errorf("%s env %q points into $HOME", c, e)
			}
		}
	}
}

func TestWriteMiseConfigRejectsInvalidTOML(t *testing.T) {
	home := t.TempDir()
	if err := writeFile(filepath.Join(home, ".config/mise/config.toml"), []byte("[tools\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := writeMiseConfig(context.Background(), runnertest.New(), nil, home, filepath.Join(t.TempDir(), "config.toml"))
	if err == nil || !strings.Contains(err.Error(), "not valid TOML") {
		t.Errorf("err = %v, want a TOML error", err)
	}
}

func TestWriteMiseConfigWithoutGlobalConfig(t *testing.T) {
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "")
	dst := filepath.Join(t.TempDir(), "config.toml")
	tools := map[string][]miseTool{"jq": {tool("1.7.1", "1.7.1", true)}}
	unpinned, err := writeMiseConfig(context.Background(), runnertest.New(), tools, t.TempDir(), dst)
	if err != nil || len(unpinned) != 0 {
		t.Fatalf("unpinned %v, err %v", unpinned, err)
	}
	if got := read(t, dst); got != "" {
		t.Errorf("copy = %q, want empty", got)
	}
}

func TestGlobalConfigPath(t *testing.T) {
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "")
	src := func(path string) miseTool {
		var m miseTool
		m.Source.Path = path
		return m
	}
	tools := map[string][]miseTool{"a": {src("/x.toml")}, "b": {src("/y.toml"), src("/y.toml")}, "c": {src("/z.tool-versions")}}
	if got := globalConfigPath(tools, "/home"); got != "/y.toml" {
		t.Errorf("got %q, want /y.toml", got)
	}
	if got := globalConfigPath(nil, "/home"); got != "/home/.config/mise/config.toml" {
		t.Errorf("got %q", got)
	}
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "/custom.toml")
	if got := globalConfigPath(nil, "/home"); got != "/custom.toml" {
		t.Errorf("got %q", got)
	}
}
