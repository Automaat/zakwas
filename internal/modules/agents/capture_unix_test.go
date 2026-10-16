//go:build !windows

package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/config"
)

func TestCaptureFailsCodexScanOnASpecialRulesFile(t *testing.T) {
	m, fake := newCodexModule(t, config.Agents{})
	rules := filepath.Join(m.CodexHome, "rules", "default.rules")
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(rules, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(m.CodexHome, "config.toml"), "")
	fake.OnOK(codexMarketList, `{"marketplaces":[]}`)
	fake.OnOK(codexPluginList, `{"installed": [], "available": []}`)
	done := make(chan error, 1)
	go func() {
		_, err := m.Capture(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "codex:") || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("capture blocks on a FIFO")
	}
}
