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
		{"agents", Windows, true},
		{"files", Windows, true},
		{"templates", Windows, true},
		{"links", Windows, true},
		{"commands", Windows, true},
		{"system", Windows, false},
		{"brew", Windows, false},
		{"mise", Windows, false},
		{"launchd", Windows, false},
		{"defaults", Windows, false},
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
		{"module on windows", ModuleError("brew", Windows), "brew: not supported on Windows (it bootstraps Homebrew for macOS and manages casks); see docs/config.md#platform-support"},
		{"windows runs links", ModuleError("links", Windows), ""},
		{"windows runs commands", ModuleError("commands", Windows), ""},
		{"module with its own windows reason", ModuleError("mise", Windows), "mise: not supported on Windows (it has no Windows bootstrap yet); see docs/config.md#platform-support"},
		{"windows runs skills", FeatureError("agents.skills", Windows), ""},
		{"windows touch id", FeatureError("system.sudoTouchID", Windows), "system.sudoTouchID: not supported on Windows (Touch ID and pam_tid.so are macOS only); see docs/config.md#platform-support"},
		{"windows keeps immutable as a fallback", FeatureError("protect.immutable", Windows), ""},
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

func TestWindowsModuleLists(t *testing.T) {
	if got, want := SupportedModules(Windows), []string{"files", "links", "templates", "agents", "commands"}; !slices.Equal(got, want) {
		t.Errorf("SupportedModules(windows) = %v, want %v", got, want)
	}
	if got, want := UnsupportedModules(Windows), []string{"system", "brew", "mise", "launchd", "defaults"}; !slices.Equal(got, want) {
		t.Errorf("UnsupportedModules(windows) = %v, want %v", got, want)
	}
}

func TestEveryUnsupportedModuleGivesAReasonOnEveryOS(t *testing.T) {
	for _, m := range Modules {
		for _, goos := range []string{Darwin, Linux, Windows} {
			if !slices.Contains(m.OS, goos) && m.Reason(goos) == "" {
				t.Errorf("%s is unsupported on %s without a reason", m.Name, goos)
			}
		}
	}
}

func TestNames(t *testing.T) {
	if Name(Windows) != "Windows" || Machine(Windows) != "machine" {
		t.Errorf("Name = %q, Machine = %q", Name(Windows), Machine(Windows))
	}
}
