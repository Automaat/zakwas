package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Automaat/zakwas/internal/runner/runnertest"
)

func TestGlobalMiseConfigPreservesOptions(t *testing.T) {
	home := t.TempDir()
	global := filepath.Join(home, ".config/mise/config.toml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	in := `[settings]
experimental = true
python.uv_venv_auto = true

[tools]
node = "22"   # lts
"pipx:black" = { version = "latest", uvx = false }
python = ["3.12", "3.11"]
go = 'latest'
unused = "1.0"

[tools."aqua:cli/cli"]
version = "2"
os = ["macos"]

[env]
version = "not a tool"
`
	want := `[settings]
experimental = true
python.uv_venv_auto = true

[tools]
jq = "1.7.1"
node = "22.1.0"   # lts
"pipx:black" = { version = "24.1.0", uvx = false }
python = ["3.12.4", "3.11.9"]
go = "1.27.1"
unused = "1.0"

[tools."aqua:cli/cli"]
version = "2.1.0"
os = ["macos"]

[env]
version = "not a tool"
`
	if err := os.WriteFile(global, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `"source":{"type":"mise.toml","path":"` + global + `"}`
	ls := `{
		"node":[{"version":"22.1.0",` + src + `}],
		"pipx:black":[{"version":"24.1.0",` + src + `}],
		"python":[{"version":"3.12.4",` + src + `},{"version":"3.11.9",` + src + `}],
		"go":[{"version":"1.27.1",` + src + `}],
		"aqua:cli/cli":[{"version":"2.1.0",` + src + `}],
		"jq":[{"version":"1.7.1","source":{"type":"mise.toml","path":"/elsewhere/conf.d/extra.toml"}}]
	}`
	fake := runnertest.New().OnOK(miseLs, ls)
	got, err := globalMiseConfig(context.Background(), fake, filepath.Join(t.TempDir(), "other-home"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("config =\n%s\nwant\n%s", got, want)
	}
}

func TestPinToolsWithoutToolsSection(t *testing.T) {
	pins := map[string][]string{"jq": {`"1.7.1"`}}
	for in, want := range map[string]string{
		"":                                  "[tools]\njq = \"1.7.1\"\n",
		"[settings]\nexperimental = true\n": "[settings]\nexperimental = true\n\n[tools]\njq = \"1.7.1\"\n",
		"[settings]\nexperimental = true":   "[settings]\nexperimental = true\n\n[tools]\njq = \"1.7.1\"\n",
	} {
		if got := pinTools(in, pins); got != want {
			t.Errorf("pinTools(%q) = %q, want %q", in, got, want)
		}
	}
}
