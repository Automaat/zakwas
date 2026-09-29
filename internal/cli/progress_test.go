package cli

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
)

func TestProgress(t *testing.T) {
	quiet := engine.Step{N: 1, Total: 2, Module: "files", Change: engine.Change{Action: engine.Create, Target: "~/.zshrc"}}
	loud := engine.Step{N: 2, Total: 2, Module: "brew", Change: engine.Change{Action: engine.Run, Target: "brew bundle install", Streams: true}}
	tests := []struct {
		name   string
		groups bool
		err    error
		want   string
	}{
		{"quiet on one line, loud framed", false, nil, "[1/2] files + ~/.zshrc ✓ 12ms\n" +
			"[2/2] brew ▶ brew bundle install\n" +
			"[2/2] brew ▶ brew bundle install ✓ 1.5s\n"},
		{"GitHub Actions groups", true, nil, "[1/2] files + ~/.zshrc ✓ 12ms\n" +
			"::group::[2/2] brew ▶ brew bundle install\n" +
			"::endgroup::\n" +
			"[2/2] brew ▶ brew bundle install ✓ 1.5s\n"},
		{"failure", false, errors.New("x"), "[1/2] files + ~/.zshrc ✗ failed\n" +
			"[2/2] brew ▶ brew bundle install\n" +
			"[2/2] brew ▶ brew bundle install ✗ failed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := &progress{out: &console{w: &buf}, groups: tt.groups}
			p.Start(quiet)
			p.Done(quiet, 12*time.Millisecond, tt.err)
			p.Start(loud)
			p.Done(loud, 1512*time.Millisecond, tt.err)
			if buf.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", buf.String(), tt.want)
			}
		})
	}
}

func TestRecap(t *testing.T) {
	for r, want := range map[engine.Result]string{
		{Applied: 3, Duration: 400 * time.Microsecond}:                 "Applied 3 of 3 steps in <1ms.",
		{Applied: 1, Failed: 1, Skipped: 2, Duration: 2 * time.Second}: "Applied 1 of 4 steps in 2s. 1 failed, 2 skipped.",
	} {
		if got := recap(r, engine.Style{}); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
