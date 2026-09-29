//go:build integration

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/runner"
)

const realMiseConfig = `[settings]
experimental = true

[tools]
nodejs = "22"   # lts
golang = "latest"
shellcheck = { version = "0.11", os = ["macos"] }
python = [
  "3.12", # main
  "3.11",
]
jq = "1.7.1"

[[watch_files]]
patterns = ["*.toml"]
run = "echo"

[env]
NOTE = """
[tools]
version = '1'
"""
`

// TestRealMisePinsGlobalConfig runs init's pinning against real mise with
// fake installs in an isolated data dir: aliases, options, multi-line
// arrays and other sections must survive, pinned to the installed versions.
func TestRealMisePinsGlobalConfig(t *testing.T) {
	if _, err := exec.LookPath("mise"); err != nil {
		t.Skip("mise not on PATH")
	}
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	t.Setenv("MISE_DATA_DIR", data)
	t.Setenv("MISE_STATE_DIR", t.TempDir())
	t.Setenv("MISE_CACHE_DIR", t.TempDir())
	t.Setenv("MISE_OFFLINE", "1")
	t.Setenv("MISE_AUTO_INSTALL", "0")
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "")
	files := map[string]string{
		filepath.Join(home, ".config/mise/config.toml"):               realMiseConfig,
		filepath.Join(data, "installs/jq/.mise.backend.toml"):         "short = \"jq\"\nfull = \"aqua:jqlang/jq\"\n",
		filepath.Join(data, "installs/shellcheck/.mise.backend.toml"): "short = \"shellcheck\"\nfull = \"aqua:koalaman/shellcheck\"\n",
	}
	for _, v := range []string{"node/22.21.1", "go/1.27.1", "python/3.12.4", "python/3.11.9", "jq/1.7.1", "shellcheck/0.11.0"} {
		files[filepath.Join(data, "installs", v, "bin/.keep")] = ""
	}
	for path, body := range files {
		if err := writeFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r := runner.NewExec()
	tools, err := miseTools(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dotfiles/mise/config.toml")
	unpinned, err := writeMiseConfig(context.Background(), r, tools, home, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(unpinned) != 0 {
		t.Errorf("unpinned = %v", unpinned)
	}
	got := read(t, dst)
	for _, want := range []string{
		`node = "22.21.1"   # lts`,
		`go = "1.27.1"`,
		`shellcheck = { version = "0.11.0", os = ["macos"] }`,
		"python = [\n  \"3.12.4\", # main\n  \"3.11.9\",\n]",
		`jq = "1.7.1"`,
		"[settings]\nexperimental = true\n",
		"[[watch_files]]\npatterns = [\"*.toml\"]\nrun = \"echo\"\n",
		"NOTE = \"\"\"\n[tools]\nversion = '1'\n\"\"\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pinned config lacks %q:\n%s", want, got)
		}
	}
	for _, alias := range []string{"nodejs", "golang"} {
		if regexp.MustCompile(`(?m)^` + alias + `\s*=`).MatchString(got) {
			t.Errorf("alias %s kept next to its pinned entry:\n%s", alias, got)
		}
	}
	var parsed struct {
		Tools map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range parsed.Tools {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"go", "jq", "node", "python", "shellcheck"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/state")); err == nil {
		t.Error("mise wrote state under $HOME")
	}
}
