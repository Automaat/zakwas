package capture

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/proposal"
)

const base = `agents:
  providers: [claude, codex]
  marketplaces:
    market: owner/repo
`

func repo(t *testing.T) (*config.Config, string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "zakwas.yaml")
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path, home)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, home
}

func market(provider, name, source string) agents.Candidate {
	return agents.Candidate{Provider: provider, Kind: agents.KindMarketplace, Name: name, Source: source}
}

func rule(provider, decision, command string) agents.Candidate {
	return agents.Candidate{Provider: provider, Kind: agents.KindPermission, Name: command, Decision: decision}
}

func server(provider string, s agents.CapturedServer) agents.Candidate {
	return agents.Candidate{Provider: provider, Kind: agents.KindMCP, Name: "docs", Server: &s}
}

func summary(res Result) []string {
	var out []string
	for _, op := range res.Ops {
		out = append(out, op.Summary()+" "+strings.Join(op.Providers, ","))
	}
	return out
}

func skips(res Result) map[string]string {
	out := map[string]string{}
	for _, c := range res.Skipped {
		out[c.Provider+" "+c.Kind+" "+c.Name] = c.Skip
	}
	return out
}

func TestPlanMergesProvidersAndOrdersOperations(t *testing.T) {
	cfg, home := repo(t)
	res := Plan([]agents.Candidate{
		rule("claude", "allow", "git status"),
		{Provider: "claude", Kind: agents.KindPlugin, Name: "a@fresh"},
		server("codex", agents.CapturedServer{Command: "npx", EnvVars: []string{"B", "A"}}),
		market("codex", "fresh", "https://github.com/o/fresh.git"),
		market("claude", "fresh", "o/fresh"),
		rule("codex", "allow", "git status"),
		server("claude", agents.CapturedServer{Command: "npx", EnvVars: []string{"A", "B"}}),
		{Provider: "codex", Kind: agents.KindPermission, Name: "rm -rf", Decision: "deny", Justification: "no"},
	}, cfg, home)
	want := []string{
		"add marketplace fresh claude,codex",
		"add plugin a@fresh claude",
		"add MCP server docs claude,codex",
		`allow "git status" claude,codex`,
		`deny "rm -rf" codex`,
	}
	if got := summary(res); !slices.Equal(got, want) {
		t.Errorf("ops\n got %q\nwant %q", got, want)
	}
	if len(res.Findings) != 8 || len(res.Skipped) != 0 {
		t.Errorf("%d findings, skipped %v", len(res.Findings), skips(res))
	}
	if res.Ops[4].Justification != "no" {
		t.Errorf("justification = %q", res.Ops[4].Justification)
	}
	if _, err := proposal.Apply(res.Ops, cfg, home); err != nil {
		t.Errorf("the operations don't apply together: %v", err)
	}
}

func TestPlanSkipsWhatCannotBeShared(t *testing.T) {
	cfg, home := repo(t)
	res := Plan([]agents.Candidate{
		market("claude", "fresh", "o/fresh"),
		market("codex", "fresh", "o/other"),
		server("claude", agents.CapturedServer{Command: "a"}),
		server("codex", agents.CapturedServer{Command: "b"}),
		{Provider: "codex", Kind: agents.KindPlugin, Name: "x@fresh", Skip: "installed but disabled"},
	}, cfg, home)
	if got := summary(res); !slices.Equal(got, []string{"add marketplace fresh claude", "add MCP server docs claude"}) {
		t.Errorf("ops = %q", got)
	}
	got := skips(res)
	if !strings.Contains(got["codex marketplace fresh"], "comes from o/other, but claude's marketplace of that name comes from o/fresh") ||
		!strings.Contains(got["codex mcp docs"], "differs from claude's server") ||
		got["codex plugin x@fresh"] != "installed but disabled" {
		t.Errorf("skipped = %v", got)
	}
}

func TestPlanDropsOperationsTheConfigRejects(t *testing.T) {
	cfg, home := repo(t)
	res := Plan([]agents.Candidate{
		market("claude", "market", "o/again"),
		{Provider: "claude", Kind: agents.KindPlugin, Name: "a@market"},
		{Provider: "claude", Kind: agents.KindPlugin, Name: "b@nowhere"},
		rule("claude", "allow", "rm -rf *"),
		rule("claude", "allow", "ls"),
		rule("claude", "deny", "ls"),
		rule("claude", "allow", "git status"),
	}, cfg, home)
	if got := summary(res); !slices.Equal(got, []string{"add plugin a@market claude", `deny "ls" claude`, `allow "git status" claude`}) {
		t.Errorf("ops = %q", got)
	}
	got := skips(res)
	for key, want := range map[string]string{
		"claude marketplace market":  "already declared",
		"claude plugin b@nowhere":    `marketplace "nowhere" isn't declared for claude`,
		"claude permission rm -rf *": "contains '*'",
	} {
		if !strings.Contains(got[key], want) || (!strings.HasPrefix(got[key], "can't be declared: ") && !strings.Contains(key, "plugin")) {
			t.Errorf("%s: %q, want %q", key, got[key], want)
		}
	}
	if _, err := proposal.Apply(res.Ops, cfg, home); err != nil {
		t.Errorf("the operations don't apply together: %v", err)
	}
}

func TestPlanWithNothingToDeclare(t *testing.T) {
	cfg, home := repo(t)
	res := Plan(nil, cfg, home)
	if len(res.Ops) != 0 || len(res.Findings) != 0 || len(res.Skipped) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestPlanKeepsTheStrictestDecisionOfACommand(t *testing.T) {
	cfg, home := repo(t)
	res := Plan([]agents.Candidate{
		rule("codex", "allow", "git push"),
		rule("codex", "review", "git push"),
		rule("codex", "deny", "git push"),
		rule("claude", "allow", "git push"),
	}, cfg, home)
	if got := summary(res); !slices.Equal(got, []string{`deny "git push" codex`, `allow "git push" claude`}) {
		t.Errorf("ops = %q", got)
	}
	if len(res.Skipped) != 2 || !strings.Contains(res.Skipped[0].Skip, "stricter decision") {
		t.Errorf("skipped = %v", skips(res))
	}
}

func TestPlanKeepsAPluginForTheProviderWhoseMarketplaceIsDeclared(t *testing.T) {
	cfg, home := repo(t)
	res := Plan([]agents.Candidate{
		market("claude", "diff", "o/diffA"),
		market("codex", "diff", "o/diffB"),
		{Provider: "claude", Kind: agents.KindPlugin, Name: "q@diff"},
		{Provider: "codex", Kind: agents.KindPlugin, Name: "q@diff"},
		{Provider: "claude", Kind: agents.KindPlugin, Name: "p@market"},
		{Provider: "codex", Kind: agents.KindPlugin, Name: "p@market"},
	}, cfg, home)
	want := []string{"add marketplace diff claude", "add plugin q@diff claude", "add plugin p@market claude,codex"}
	if got := summary(res); !slices.Equal(got, want) {
		t.Errorf("ops\n got %q\nwant %q", got, want)
	}
	if why := skips(res)["codex plugin q@diff"]; !strings.Contains(why, `"diff" isn't declared for codex`) {
		t.Errorf("skipped = %v", skips(res))
	}
	if _, err := proposal.Apply(res.Ops, cfg, home); err != nil {
		t.Errorf("the operations don't apply together: %v", err)
	}
}
