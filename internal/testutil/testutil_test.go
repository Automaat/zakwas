package testutil

import (
	"errors"
	"runtime"
	"testing"
)

type recorder struct {
	testing.TB
	skipped, failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Skipf(string, ...any)  { r.skipped = true }
func (r *recorder) Fatalf(string, ...any) { r.failed = true }

func TestRequireSymlinks(t *testing.T) {
	denied := errors.New("A required privilege is not held by the client")
	tests := []struct {
		name    string
		denied  error
		ci      bool
		skipped bool
		failed  bool
	}{
		{"allowed", nil, false, false, false},
		{"allowed in CI", nil, true, false, false},
		{"denied on a desktop skips", denied, false, true, false},
		{"denied in CI fails", denied, true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &recorder{}

			requireSymlinks(r, tt.denied, tt.ci)

			if r.skipped != tt.skipped || r.failed != tt.failed {
				t.Errorf("skipped = %v, failed = %v; want %v, %v", r.skipped, r.failed, tt.skipped, tt.failed)
			}
		})
	}
}

func TestInCI(t *testing.T) {
	tests := []struct {
		ci, actions string
		want        bool
	}{
		{"", "", false},
		{"true", "", true},
		{"", "true", true},
		{"false", "", false},
	}
	for _, tt := range tests {
		t.Setenv("CI", tt.ci)
		t.Setenv("GITHUB_ACTIONS", tt.actions)
		if got := InCI(); got != tt.want {
			t.Errorf("CI=%q GITHUB_ACTIONS=%q: InCI = %v, want %v", tt.ci, tt.actions, got, tt.want)
		}
	}
}

func TestSymlinkDeniedIsOnlyAWindowsThing(t *testing.T) {
	if runtime.GOOS != "windows" && SymlinkDenied() != nil {
		t.Errorf("SymlinkDenied = %v off Windows", SymlinkDenied())
	}
}
