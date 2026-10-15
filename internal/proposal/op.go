// Package proposal holds saved edits to the config repo: typed operations
// that change zakwas.yaml, the Brewfile or the mise config byte-for-byte
// where they don't touch, with the plan and risk tier they were judged by.
package proposal

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
)

// Operation kinds.
const (
	AddPlugin        = "add-plugin"
	AddPermission    = "add-permission"
	RemovePermission = "remove-permission"
	AddBrew          = "add-brew"
	PinMise          = "pin-mise"
	AddMCP           = "add-mcp"
)

// Kinds lists the operations in the order they are documented.
var Kinds = []string{AddPlugin, AddPermission, RemovePermission, AddBrew, PinMise, AddMCP}

// Op is one typed edit. Which fields it uses depends on Kind: Name is the
// plugin id, MCP server name, brew package or mise tool.
type Op struct {
	Kind          string   `json:"kind"`
	Name          string   `json:"name,omitempty"`
	Decision      string   `json:"decision,omitempty"`
	Command       string   `json:"command,omitempty"`
	Justification string   `json:"justification,omitempty"`
	Version       string   `json:"version,omitempty"`
	Cask          bool     `json:"cask,omitempty"`
	Providers     []string `json:"providers,omitempty"`
	Server        *Server  `json:"server,omitempty"`
}

// Server is the MCP server an add-mcp operation declares.
type Server struct {
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	URL            string   `json:"url,omitempty"`
	EnvVars        []string `json:"env_vars,omitempty"`
	BearerTokenEnv string   `json:"bearer_token_env,omitempty"`
}

var (
	brewName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._+/-]*$`)
	miseTool = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:@._/-]*$`)
	miseVer  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	envName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Validate checks the operation's own fields; whether the edit fits the
// config is checked when it is applied.
func (o Op) Validate() error {
	var errs []error
	need := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}
	switch o.Kind {
	case AddPlugin:
		name, mkt, ok := strings.Cut(o.Name, "@")
		need(ok && name != "" && mkt != "" && !strings.Contains(mkt, "@"), "plugin id must be name@marketplace")
	case AddPermission, RemovePermission:
		need(slices.Contains([]string{config.PermissionAllow, config.PermissionReview, config.PermissionDeny}, o.Decision), "decision must be allow, review or deny")
		need(len(strings.Fields(o.Command)) > 0, "a command is required")
		if o.Kind == RemovePermission {
			need(len(o.Providers) == 0 && o.Justification == "", "remove-permission takes only a decision and a command")
		}
	case AddBrew:
		need(brewName.MatchString(o.Name), "package name has unsupported characters")
	case PinMise:
		need(miseTool.MatchString(o.Name), "tool name has unsupported characters")
		need(miseVer.MatchString(o.Version), "version has unsupported characters")
	case AddMCP:
		errs = append(errs, o.validateMCP()...)
	default:
		return fmt.Errorf("unknown operation %q; use one of %s", o.Kind, strings.Join(Kinds, ", "))
	}
	for _, p := range o.Providers {
		need(slices.Contains(config.Providers, p), fmt.Sprintf("unknown provider %q", p))
	}
	if o.Kind != AddBrew {
		need(!o.Cask, "only add-brew takes a cask")
	}
	for _, s := range []string{o.Name, o.Command, o.Justification, o.Version} {
		need(!strings.ContainsAny(s, "\r\n"), "values must be one line")
	}
	return errors.Join(errs...)
}

func (o Op) validateMCP() []error {
	var errs []error
	if !nameRe.MatchString(o.Name) {
		errs = append(errs, errors.New("server name has unsupported characters"))
	}
	s := o.Server
	if s == nil || (s.Command == "") == (s.URL == "") {
		return append(errs, errors.New("set either a command or a url"))
	}
	for _, v := range append(slices.Clone(s.Args), s.Command, s.URL) {
		if strings.ContainsAny(v, "\r\n") {
			errs = append(errs, errors.New("values must be one line"))
			break
		}
	}
	for _, v := range append(slices.Clone(s.EnvVars), s.BearerTokenEnv) {
		if v != "" && !envName.MatchString(v) {
			errs = append(errs, fmt.Errorf("%q is not an environment variable name", v))
		}
	}
	if s.URL != "" && (len(s.Args) > 0 || len(s.EnvVars) > 0) {
		errs = append(errs, errors.New("args and env vars belong to a command server"))
	}
	if s.Command != "" && s.BearerTokenEnv != "" {
		errs = append(errs, errors.New("a bearer token belongs to a url server"))
	}
	return errs
}

// Summary is the operation in one line.
func (o Op) Summary() string {
	switch o.Kind {
	case AddPlugin:
		return "add plugin " + o.Name
	case AddPermission:
		return fmt.Sprintf("%s %q", o.Decision, o.Command)
	case RemovePermission:
		return fmt.Sprintf("remove %s rule %q", o.Decision, o.Command)
	case AddBrew:
		if o.Cask {
			return "add brew cask " + o.Name
		}
		return "add brew package " + o.Name
	case PinMise:
		return fmt.Sprintf("pin mise tool %s to %s", o.Name, o.Version)
	case AddMCP:
		return "add MCP server " + o.Name
	}
	return o.Kind
}

// Modules are the modules whose plan an operation changes, in apply order.
func (o Op) Modules() []string {
	switch o.Kind {
	case AddPermission, RemovePermission, AddMCP:
		return []string{"templates", "agents"}
	case AddPlugin:
		return []string{"agents"}
	case AddBrew:
		return []string{"brew"}
	case PinMise:
		return []string{"files", "mise"}
	}
	return nil
}

// Summaries joins the operations' one-line summaries.
func Summaries(ops []Op) string {
	s := make([]string, len(ops))
	for i, o := range ops {
		s[i] = o.Summary()
	}
	return strings.Join(s, "; ")
}

// ModulesOf is the union of the operations' modules, in the given order.
func ModulesOf(ops []Op, order []string) []string {
	var out []string
	for _, m := range order {
		if slices.ContainsFunc(ops, func(o Op) bool { return slices.Contains(o.Modules(), m) }) {
			out = append(out, m)
		}
	}
	return out
}
