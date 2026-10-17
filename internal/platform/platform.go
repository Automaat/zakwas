// Package platform is the single source of truth for which operating
// systems each zakwas module and feature supports.
package platform

import (
	"fmt"
	"runtime"
	"slices"
)

const (
	Darwin  = "darwin"
	Linux   = "linux"
	Windows = "windows"
)

const DocsAnchor = "docs/config.md#platform-support"

var (
	allOS      = []string{Darwin, Linux, Windows}
	unixOnly   = []string{Darwin, Linux}
	darwinOnly = []string{Darwin}
)

// Module is a zakwas module and the operating systems it runs on. Why says
// why it is missing elsewhere; WhyOS overrides it for one OS.
type Module struct {
	Name  string
	OS    []string
	Why   string
	WhyOS map[string]string
}

// Reason is why the module is not supported on goos.
func (m Module) Reason(goos string) string {
	if why, ok := m.WhyOS[goos]; ok {
		return why
	}
	return m.Why
}

// Modules lists every module in apply order.
var Modules = []Module{
	{Name: "system", OS: unixOnly, Why: "its directory modes and ssh-keygen setup are not ported to Windows yet"},
	{Name: "files", OS: allOS},
	{Name: "links", OS: unixOnly, Why: "symlinks need a privilege Windows users lack by default and have no fallback yet"},
	{Name: "templates", OS: allOS},
	{Name: "brew", OS: darwinOnly, Why: "it bootstraps Homebrew for macOS and manages casks"},
	{Name: "mise", OS: darwinOnly, Why: "its bootstrap installs the macOS build of mise", WhyOS: map[string]string{Windows: "it has no Windows bootstrap yet"}},
	{Name: "agents", OS: allOS},
	{Name: "commands", OS: unixOnly, Why: "its checks and steps run with sh, which Windows lacks"},
	{Name: "launchd", OS: darwinOnly, Why: "launchd is macOS's service manager"},
	{Name: "defaults", OS: darwinOnly, Why: "it drives the macOS 'defaults' tool"},
}

// Feature is an option that is available on fewer operating systems than
// the rest of its section. Without a Fallback, using it elsewhere is a
// config error; with one, zakwas does that instead and warns in the plan.
type Feature struct {
	Name     string
	OS       []string
	Why      string
	Fallback string
}

var Features = []Feature{
	{Name: "system.sudoTouchID", OS: darwinOnly, Why: "Touch ID and pam_tid.so are macOS only"},
	{Name: "agents.skills", OS: unixOnly, Why: "skills are linked with symlinks, which Windows does not support yet"},
	{Name: "protect.immutable", OS: darwinOnly, Why: "the uchg flag is macOS only", Fallback: "files are read-only copies"},
}

// Current is the operating system this binary runs on.
func Current() string { return runtime.GOOS }

// Name is the user-facing spelling of an operating system.
func Name(goos string) string {
	switch goos {
	case Darwin:
		return "macOS"
	case Linux:
		return "Linux"
	case Windows:
		return "Windows"
	}
	return goos
}

// Machine is what messages call the computer zakwas runs on.
func Machine(goos string) string {
	if goos == Darwin {
		return "Mac"
	}
	return "machine"
}

// Supported reports whether module runs on goos. An unknown module is
// unsupported everywhere.
func Supported(module, goos string) bool {
	for _, m := range Modules {
		if m.Name == module {
			return slices.Contains(m.OS, goos)
		}
	}
	return false
}

// FeatureSupported reports whether the named feature works on goos.
func FeatureSupported(feature, goos string) bool {
	for _, f := range Features {
		if f.Name == feature {
			return slices.Contains(f.OS, goos)
		}
	}
	return false
}

// Names lists every module in apply order.
func Names() []string {
	out := make([]string, len(Modules))
	for i, m := range Modules {
		out[i] = m.Name
	}
	return out
}

// SupportedModules lists the modules that run on goos, in apply order.
func SupportedModules(goos string) []string {
	var out []string
	for _, m := range Modules {
		if slices.Contains(m.OS, goos) {
			out = append(out, m.Name)
		}
	}
	return out
}

// UnsupportedModules lists the modules that do not run on goos.
func UnsupportedModules(goos string) []string {
	var out []string
	for _, m := range Modules {
		if !slices.Contains(m.OS, goos) {
			out = append(out, m.Name)
		}
	}
	return out
}

// ModuleError is the one-line complaint for configuring module on goos, or
// nil when it is supported.
func ModuleError(module, goos string) error {
	for _, m := range Modules {
		if m.Name != module || slices.Contains(m.OS, goos) {
			continue
		}
		return fmt.Errorf("%s: not supported on %s (%s); see %s", module, Name(goos), m.Reason(goos), DocsAnchor)
	}
	return nil
}

// FeatureError is the one-line complaint for using feature on goos, or nil
// when it is supported or has a fallback.
func FeatureError(feature, goos string) error {
	for _, f := range Features {
		if f.Name != feature || slices.Contains(f.OS, goos) || f.Fallback != "" {
			continue
		}
		return fmt.Errorf("%s: not supported on %s (%s); see %s", feature, Name(goos), f.Why, DocsAnchor)
	}
	return nil
}
