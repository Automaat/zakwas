package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/Automaat/zakwas/internal/install"
)

func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"zakwas": func() { os.Exit(run()) },
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
			env.Setenv("PATH", bin+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("ZAKWAS_PAM_FILE", filepath.Join(env.WorkDir, "sudo_local"))
			return nil
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
