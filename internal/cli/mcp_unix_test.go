//go:build !windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/proposal"
)

func TestMCPApplyStopsOnSIGTERMAndPutsTheFilesBack(t *testing.T) {
	env, home := brewSetup(t)
	installing := make(chan struct{})
	env.Runner = brewRunner{gitRunner: env.Runner.(brewRunner).gitRunner, install: func(ctx context.Context) {
		close(installing)
		<-ctx.Done()
	}}
	c := startMCP(t, env, formClient, "--enable-apply")
	id := parseJSON(t, c.tool("zakwas_propose", map[string]any{"kind": "add-brew", "name": "jq"}, nil).text)["id"].(string)
	brewfile := readFile(t, filepath.Join(env.Cwd, "Brewfile"))
	go func() {
		<-installing
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
		}
	}()

	r := c.tool("zakwas_apply", map[string]any{"id": id}, accept)

	if out := parseJSON(t, r.text); out["applied"] == true {
		t.Errorf("outcome = %v", out)
	}
	if got := readFile(t, filepath.Join(env.Cwd, "Brewfile")); got != brewfile {
		t.Errorf("Brewfile wasn't put back:\n%s", got)
	}
	if gitIn(t, env.Cwd, "status", "--porcelain") != "" {
		t.Error("the repo is dirty")
	}
	if _, err := os.Stat(filepath.Join(proposal.Dir(home), id+".json")); err != nil {
		t.Errorf("the proposal is gone: %v", err)
	}
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server ignored SIGTERM")
	}
}

func TestMCPStopsCleanlyOnSIGHUPAndSIGTERM(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			env, _ := setup(t, linksOnly)
			c := startMCP(t, env, formClient)
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-c.done:
			case <-time.After(10 * time.Second):
				t.Fatalf("the server ignored %v", sig)
			}
			if c.code != ExitOK {
				t.Errorf("exit %d, want %d", c.code, ExitOK)
			}
		})
	}
}
