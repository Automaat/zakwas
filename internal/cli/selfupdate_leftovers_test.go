package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner/runnertest"
	"github.com/Automaat/zakwas/internal/selfupdate"
	"github.com/Automaat/zakwas/internal/selfupdate/selfupdatetest"
)

const leftoverConfig = "files: [{src: dotfiles/zshrc, dst: ~/.zshrc}]\n"

// leftover is an install that a Windows self-update has run on: zakwas.exe
// is the new binary and its old copy sits in a recorded directory.
func leftover(t *testing.T) (env Env, exe, old string) {
	t.Helper()
	env, home := setup(t, leftoverConfig)
	exe = filepath.Join(t.TempDir(), "zakwas.exe")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := selfupdate.Swapper{StatePath: selfupdate.StatePath(home)}
	if err := w.ReplaceRunning(exe, []byte("new binary")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".zakwas-old-") {
			old = filepath.Join(filepath.Dir(exe), e.Name(), "zakwas.exe")
		}
	}
	if old == "" {
		t.Fatal("the swap left no old copy")
	}
	env.Executable, env.Runner = exe, runnertest.New().Missing("gh")
	return env, exe, old
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestApplyAndUpgradeCleanASelfUpdateLeftoverOnWindowsOnly(t *testing.T) {
	for _, tt := range []struct {
		goos      string
		cmd       []string
		wantClean bool
	}{
		{platform.Windows, []string{"apply", "-y", "--only", "files"}, true},
		{platform.Darwin, []string{"apply", "-y", "--only", "files"}, false},
		{platform.Linux, []string{"apply", "-y", "--only", "files"}, false},
	} {
		t.Run(tt.goos, func(t *testing.T) {
			env, exe, old := leftover(t)
			env.GOOS = tt.goos

			r := invoke(env, "", tt.cmd...)

			if r.code != ExitOK {
				t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
			if exists(old) == tt.wantClean {
				t.Errorf("old copy exists = %v after %v on %s", exists(old), tt.cmd, tt.goos)
			}
			if got := read(t, exe); got != "new binary" {
				t.Errorf("exe = %q", got)
			}
		})
	}
}

func TestApplyWarnsAndGoesOnWhenTheSelfUpdateRecordIsDamaged(t *testing.T) {
	env, _, old := leftover(t)
	env.GOOS = platform.Windows
	state := selfupdate.StatePath(env.Home)
	if err := os.WriteFile(state, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := invoke(env, "", "apply", "-y", "--only", "files")

	if r.code != ExitOK || !strings.Contains(r.stderr, "warning: ") || !strings.Contains(r.stderr, "selfupdate.json.bad") {
		t.Errorf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if !exists(state+".bad") || !exists(old) {
		t.Errorf("the damaged file was not set aside, or an unrecorded copy was deleted")
	}
}

// snapshot lists every file under dir with its size and modification time,
// and the state file's bytes.
func installSnapshot(t *testing.T, dir, state string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "%s %s %d\n", state, read(t, state), info.ModTime().UnixNano())
	return b.String()
}

func TestReadOnlyCommandsLeaveSelfUpdateLeftoversAlone(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}` + "\n"
	for name, args := range map[string][]string{
		"plan":       {"plan", "--only", "files"},
		"plan json":  {"plan", "--json", "--only", "files"},
		"check":      {"check", "--only", "files"},
		"check off":  {"check", "--offline"},
		"completion": {"completion", "zsh"},
		"version":    {"version"},
		"mcp":        {"mcp"},
		"proposals":  {"proposals"},
		"capture":    {"capture"},
	} {
		t.Run(name, func(t *testing.T) {
			env, exe, _ := leftover(t)
			env.GOOS = platform.Windows
			state := selfupdate.StatePath(env.Home)
			before := installSnapshot(t, filepath.Dir(exe), state)

			invoke(env, initialize, args...)

			if after := installSnapshot(t, filepath.Dir(exe), state); after != before {
				t.Errorf("%s changed the install or its state:\nbefore:\n%s\nafter:\n%s", name, before, after)
			}
		})
	}
}

func TestSelfUpdateCleansLeftoversBeforeItsOwnWork(t *testing.T) {
	selfUpdateAs(t, platform.Windows)
	env, exe, old := leftover(t)
	env.Version = "1.2.3"
	env.ReleaseURL = selfupdatetest.ServeFor(t, "1.2.3", platform.Windows, runtime.GOARCH, "new binary")

	r := invoke(env, "", "self-update")

	if r.code != ExitOK || !strings.Contains(r.stdout, "already installed") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if exists(old) || read(t, exe) != "new binary" {
		t.Errorf("old copy exists = %v, exe = %q", exists(old), read(t, exe))
	}
}
