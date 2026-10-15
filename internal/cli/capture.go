package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/Automaat/zakwas/internal/capture"
	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/proposal"
)

const captureUsage = `Usage: zakwas capture [-c zakwas.yaml] [-json] [--write]

Lists, per provider, what the managed agents have that zakwas.yaml doesn't
declare: plugins, marketplaces and MCP servers added in the agents, Claude
allow rules, and the commands approved in Codex's default.rules. Nothing is
changed. Plugins that come from no marketplace (built-in and synced ones),
project and local installs, and what zakwas added itself are never listed.

With --write the findings are saved as one proposal, which is applied like
any other with 'zakwas approve ID' in a terminal. Findings that can't be
declared are listed with the reason and left out.

Flags:
`

type captureOptions struct {
	*configOptions
	write bool
}

func newCaptureFlags(errOut io.Writer) (*flag.FlagSet, *captureOptions) {
	fs, c := newConfigFlags("capture", captureUsage, true, errOut)
	o := &captureOptions{configOptions: c}
	fs.BoolVar(&o.write, "write", false, "save the findings as one proposal")
	return fs, o
}

// captureDoc is what capture reports, as JSON and for the MCP tool.
// Findings are what the operations in Ops declare, each of which is a valid
// proposal operation; Skipped can't be declared.
type captureDoc struct {
	FormatVersion string             `json:"format_version"`
	Config        string             `json:"config"`
	Findings      []agents.Candidate `json:"findings"`
	Skipped       []agents.Candidate `json:"skipped"`
	Ops           []proposal.Op      `json:"ops"`
	Proposal      *proposal.Proposal `json:"proposal,omitempty"`
}

var errNoAgents = errors.New("zakwas.yaml has no agents section to capture into")

// scanCapture reads what the managed agents have beyond zakwas.yaml. A
// provider that can't be read is an error returned along with the others'
// findings.
func scanCapture(ctx context.Context, env Env, cfg *config.Config) (captureDoc, error) {
	doc := captureDoc{FormatVersion: engine.FormatVersion, Config: cfg.Path, Findings: []agents.Candidate{}, Skipped: []agents.Candidate{}, Ops: []proposal.Op{}}
	if cfg.Agents == nil {
		return doc, errNoAgents
	}
	var mod *agents.Module
	for _, m := range Modules(cfg, env) {
		if a, ok := m.(*agents.Module); ok {
			mod = a
		}
	}
	cands, err := mod.Capture(ctx)
	res := capture.Plan(cands, cfg, env.Home)
	doc.Findings = append(doc.Findings, res.Findings...)
	doc.Skipped = append(doc.Skipped, res.Skipped...)
	doc.Ops = append(doc.Ops, res.Ops...)
	return doc, err
}

func runCapture(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, o := newCaptureFlags(errOut)
	pos, code, ok := parseSub(fs, out, args)
	if !ok {
		return code
	}
	if len(pos) != 0 {
		fs.Usage()
		return ExitUsage
	}
	cfg, err := loadConfig(o.cfgPath, env.Cwd, env.Home)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	doc, scanErr := scanCapture(ctx, env, cfg)
	if errors.Is(scanErr, errNoAgents) {
		errOut.fail(scanErr)
		return ExitErr
	}
	var ev *evaluation
	if o.write && len(doc.Ops) > 0 {
		if doc.Proposal, ev, err = saveProposal(ctx, env, cfg, doc.Ops...); err != nil {
			errOut.fail(err)
			return ExitErr
		}
	}
	switch {
	case o.asJSON:
		out.print(mustIndent(doc))
	case ev != nil:
		printFindings(out, doc)
		out.print("\n")
		if code := printProposal(out, env, doc.Proposal, ev); code != ExitOK {
			return code
		}
	default:
		printFindings(out, doc)
		if len(doc.Ops) > 0 {
			out.print("\nSave these as one proposal with: zakwas capture --write\n")
		}
	}
	if scanErr != nil {
		errOut.fail(scanErr)
		return ExitErr
	}
	return ExitOK
}

func printFindings(out *console, doc captureDoc) {
	if len(doc.Findings) == 0 {
		out.print("Nothing to capture\n")
	}
	for _, provider := range config.Providers {
		var mine []agents.Candidate
		for _, c := range doc.Findings {
			if c.Provider == provider {
				mine = append(mine, c)
			}
		}
		if len(mine) == 0 {
			continue
		}
		out.print(provider + "\n")
		for _, c := range mine {
			out.printf("  %-12s %s\n", kindLabel(c), printable(findingText(c)))
		}
	}
	if len(doc.Skipped) > 0 {
		out.print("\nNot captured\n")
		for _, c := range doc.Skipped {
			out.printf("  %s %s %s: %s\n", c.Provider, kindLabel(c), printable(c.Name), printable(c.Skip))
		}
	}
}

func kindLabel(c agents.Candidate) string {
	switch c.Kind {
	case agents.KindMCP:
		return "mcp server"
	case agents.KindPermission:
		if c.Provider == config.ProviderCodex {
			return "approval"
		}
		return "allow rule"
	}
	return c.Kind
}

func findingText(c agents.Candidate) string {
	switch {
	case c.Kind == agents.KindMarketplace:
		return c.Name + "  " + c.Source
	case c.Kind == agents.KindMCP:
		return c.Name + "  " + c.Server.Summary()
	case c.Kind == agents.KindPermission && c.Provider == config.ProviderCodex:
		return c.Decision + " " + c.Name
	}
	return c.Name
}
