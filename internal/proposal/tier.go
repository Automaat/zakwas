package proposal

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

// Risk tiers. Low proposals may be confirmed from inside an agent; high ones
// only from a terminal.
const (
	TierLow  = "low"
	TierHigh = "high"
)

// Classify rates the operations by what they are, before looking at the
// plan: a tier, and why it isn't low.
func Classify(ops []Op) (tier string, reasons []string) {
	for _, op := range ops {
		if why := op.highBecause(); why != "" {
			reasons = append(reasons, fmt.Sprintf("%s: %s", op.Summary(), why))
		}
	}
	if len(reasons) > 0 {
		return TierHigh, reasons
	}
	return TierLow, nil
}

// highBecause explains why an operation is not low risk, or returns "". A
// plugin's marketplace must be declared for the config to validate at all,
// so every plugin is from a declared marketplace, unless the same proposal
// adds it, which is rated on its own.
func (o Op) highBecause() string {
	switch o.Kind {
	case AddPlugin:
		return ""
	case AddMarketplace:
		return "a marketplace is a source of plugins that run code in every agent that installs one"
	case PinMise:
		if strings.Contains(o.Name, ":") {
			return "a tool from a mise backend (npm:, github:, cargo:, ...) runs code from outside the registry"
		}
		return ""
	case AddPermission:
		if o.Decision == config.PermissionAllow {
			return "allowing a command widens what agents run without asking"
		}
		return ""
	case RemovePermission:
		if o.Decision != config.PermissionAllow {
			return "removing a " + o.Decision + " rule loosens the rules"
		}
		return ""
	case AddBrew:
		switch {
		case o.Cask:
			return "casks install apps outside Homebrew core"
		case strings.Contains(o.Name, "/"):
			return "a package from a tap isn't Homebrew core"
		}
		return ""
	case AddMCP:
		return "an MCP server runs code or reaches a service for every agent"
	}
	return "unknown operation"
}

// Sensitive finds the paths a plan must not touch for a proposal to be low
// risk: agent config, shell rc files, ~/.ssh and /etc.
type Sensitive struct {
	Home  string
	Extra []string
}

var sensitiveHome = []string{
	".claude", ".claude.json", ".codex", ".config/opencode", ".local/share/opencode", ".agents", ".gemini", ".copilot",
	".zshrc", ".zshenv", ".zprofile", ".zlogin", ".zlogout", ".bashrc", ".bash_profile", ".bash_login", ".bash_logout", ".profile",
	".config/fish", ".ssh",
}

var sensitiveAbs = []string{"/etc", "/private/etc"}

func (s Sensitive) paths() []string {
	out := append([]string{}, sensitiveAbs...)
	for _, p := range sensitiveHome {
		out = append(out, filepath.Join(s.Home, p))
	}
	for _, p := range s.Extra {
		if p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

// Touches reports the sensitive path a change's text names, ignoring case
// since macOS volumes are case-insensitive, for a target or detail such as "~/.claude/settings.json" or "add /etc/hosts".
func (s Sensitive) Touches(text string) (string, bool) {
	paths := s.paths()
	for _, tok := range strings.Fields(text) {
		tok = strings.Trim(tok, `"'`+"`,;:()[]")
		switch {
		case tok == "~":
			tok = s.Home
		case strings.HasPrefix(tok, "~/"):
			tok = filepath.Join(s.Home, tok[2:])
		}
		if !filepath.IsAbs(tok) {
			continue
		}
		tok = strings.ToLower(filepath.Clean(tok))
		for _, p := range paths {
			if lower := strings.ToLower(p); tok == lower || strings.HasPrefix(tok, lower+string(filepath.Separator)) {
				return p, true
			}
		}
	}
	return "", false
}
