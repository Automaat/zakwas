package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/Automaat/zakwas/internal/install"
	"github.com/Automaat/zakwas/internal/testutil"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"zakwas": func() {
			goosOverride = os.Getenv("ZAKWAS_TEST_GOOS")
			os.Exit(run())
		},
	})
}

// Scripts put fake brew/mise/defaults/sudo binaries first on PATH so the
// real exec runner is exercised without touching the machine.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
		Setup: func(env *testscript.Env) error {
			home := filepath.Join(env.WorkDir, "home")
			bin := filepath.Join(env.WorkDir, "bin")
			for _, d := range []string{home, bin} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			env.Defer(func() {
				if err := install.Unlock(home); err != nil {
					env.T().Fatal(err)
				}
			})
			env.Setenv("HOME", home)
			env.Setenv("USERPROFILE", home)
			env.Setenv("PATH", bin+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("ZAKWAS_PAM_FILE", filepath.Join(env.WorkDir, "sudo_local"))
			return nil
		},
		Condition: func(cond string) (bool, error) {
			if cond == "root" {
				return os.Geteuid() == 0, nil
			}
			return false, fmt.Errorf("unknown condition %q", cond)
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"linkto": linkTo,
		},
	})
}

// linkTo asserts that args[0] is a symlink pointing at args[1].
func linkTo(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 2 {
		ts.Fatalf("usage: linkto link target")
	}
	got, err := os.Readlink(ts.MkAbs(args[0]))
	ok := err == nil && got == ts.MkAbs(args[1])
	if ok == neg {
		ts.Fatalf("%s → %q (err %v), want %s", args[0], got, err, ts.MkAbs(args[1]))
	}
}

func TestExtraPathDirs(t *testing.T) {
	tests := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/u/.local/bin"}},
		{"linux", []string{"/usr/local/bin", "/home/u/.local/bin"}},
		{"windows", []string{filepath.Join("/home/u", ".local", "bin")}},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := extraPathDirs(tt.goos, "/home/u"); !slices.Equal(got, tt.want) {
				t.Errorf("extraPathDirs(%q) = %v, want %v", tt.goos, got, tt.want)
			}
		})
	}
}

func TestExtendPathKeepsOrderAndSkipsDuplicates(t *testing.T) {
	testutil.SkipOnWindows(t, "the fixture uses POSIX PATH entries")
	t.Setenv("PATH", "/usr/local/bin:/bin")
	extendPath("/home/u")
	got := filepath.SplitList(os.Getenv("PATH"))
	if got[0] != "/usr/local/bin" || got[1] != "/bin" {
		t.Errorf("PATH reordered: %v", got)
	}
	if n := countOf(got, "/usr/local/bin"); n != 1 {
		t.Errorf("/usr/local/bin appears %d times: %v", n, got)
	}
	if !slices.Contains(got, "/home/u/.local/bin") {
		t.Errorf("~/.local/bin missing: %v", got)
	}
}

func countOf(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

func TestSamePathEntry(t *testing.T) {
	tests := []struct {
		a, b, goos string
		want       bool
	}{
		{"/usr/local/bin", "/usr/local/bin", "darwin", true},
		{"/usr/local/bin", "/USR/local/bin", "darwin", false},
		{`C:\Users\Me\.local\bin`, `c:\users\me\.local\bin`, "windows", true},
		{"C:/Users/me/bin", "C:/Users/me/bin/", "windows", true},
	}
	for _, tt := range tests {
		if got := samePathEntry(tt.a, tt.b, tt.goos); got != tt.want {
			t.Errorf("samePathEntry(%q, %q, %s) = %v, want %v", tt.a, tt.b, tt.goos, got, tt.want)
		}
	}
}
