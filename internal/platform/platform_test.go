package platform

import (
	"slices"
	"strings"
	"testing"
)

func TestSupported(t *testing.T) {
	tests := []struct {
		module, goos string
		want         bool
	}{
		{"system", Darwin, true},
		{"system", Linux, true},
		{"files", Linux, true},
		{"links", Linux, true},
		{"templates", Linux, true},
		{"agents", Linux, true},
		{"commands", Linux, true},
		{"brew", Darwin, true},
		{"brew", Linux, false},
		{"mise", Darwin, true},
		{"mise", Linux, false},
		{"defaults", Darwin, true},
		{"defaults", Linux, false},
		{"launchd", Darwin, true},
		{"launchd", Linux, false},
		{"nope", Darwin, false},
		{"agents", "windows", false},
	}
	for _, tt := range tests {
		t.Run(tt.module+"/"+tt.goos, func(t *testing.T) {
			if got := Supported(tt.module, tt.goos); got != tt.want {
				t.Errorf("Supported(%q, %q) = %v, want %v", tt.module, tt.goos, got, tt.want)
			}
		})
	}
}

func TestDarwinSupportsEveryModule(t *testing.T) {
	for _, m := range Modules {
		if !Supported(m.Name, Darwin) {
			t.Errorf("%s is not supported on darwin", m.Name)
		}
		if !slices.Contains(m.OS, Darwin) {
			t.Errorf("%s does not list darwin", m.Name)
		}
		if !slices.Contains(m.OS, Linux) && m.Why == "" {
			t.Errorf("%s is macOS only but gives no reason", m.Name)
		}
	}
}

func TestModuleLists(t *testing.T) {
	if got, want := SupportedModules(Linux), []string{"system", "files", "links", "templates", "agents", "commands"}; !slices.Equal(got, want) {
		t.Errorf("SupportedModules(linux) = %v, want %v", got, want)
	}
	if got, want := UnsupportedModules(Linux), []string{"brew", "mise", "launchd", "defaults"}; !slices.Equal(got, want) {
		t.Errorf("UnsupportedModules(linux) = %v, want %v", got, want)
	}
	if got := UnsupportedModules(Darwin); len(got) != 0 {
		t.Errorf("UnsupportedModules(darwin) = %v, want none", got)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"module on linux", ModuleError("brew", Linux), "brew: not supported on Linux (it bootstraps Homebrew for macOS and manages casks); see docs/config.md#platform-support"},
		{"supported module", ModuleError("agents", Linux), ""},
		{"module on darwin", ModuleError("brew", Darwin), ""},
		{"feature on linux", FeatureError("system.sudoTouchID", Linux), "system.sudoTouchID: not supported on Linux (Touch ID and pam_tid.so are macOS only); see docs/config.md#platform-support"},
		{"feature on darwin", FeatureError("system.sudoTouchID", Darwin), ""},
		{"feature with a fallback", FeatureError("protect.immutable", Linux), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if tt.err != nil {
				got = tt.err.Error()
			}
			if got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "\n") {
				t.Errorf("error spans several lines: %q", got)
			}
		})
	}
}

func TestFeatureSupported(t *testing.T) {
	if FeatureSupported("protect.immutable", Linux) || !FeatureSupported("protect.immutable", Darwin) {
		t.Error("protect.immutable should be darwin only")
	}
	if FeatureSupported("unknown", Darwin) {
		t.Error("unknown feature reported supported")
	}
}
