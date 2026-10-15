// Package capture turns what agents have that zakwas.yaml doesn't declare
// into proposal operations that, applied together, declare it.
package capture

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/agents"
	"github.com/Automaat/zakwas/internal/proposal"
)

// Result is what Plan makes of the candidates. Findings are the candidates
// the operations declare, Skipped the ones that can't be declared, each with
// its reason.
type Result struct {
	Findings []agents.Candidate
	Skipped  []agents.Candidate
	Ops      []proposal.Op
}

// entry is one operation with the candidates it declares.
type entry struct {
	op    proposal.Op
	cands []agents.Candidate
}

// Plan merges the candidates of several providers into one operation per
// marketplace, plugin, server and rule, and drops the operations that
// wouldn't apply to cfg, so the rest always do together.
func Plan(cands []agents.Candidate, cfg *config.Config, home string) Result {
	var res Result
	var entries []*entry
	index := map[string]*entry{}
	cands, res.Skipped = strictest(cands)
	cands = marketplacesFirst(cands)
	for _, c := range cands {
		if c.Skip == "" && c.Kind == agents.KindPlugin {
			if why := noMarketplace(c, index, cfg); why != "" {
				c.Skip = why
			}
		}
		if c.Skip != "" {
			res.Skipped = append(res.Skipped, c)
			continue
		}
		key := c.Kind + "\x00" + c.Name
		if c.Kind == agents.KindPermission {
			key += "\x00" + c.Decision
		}
		e, ok := index[key]
		if !ok {
			e = &entry{op: opOf(c)}
			index[key] = e
			entries = append(entries, e)
		} else if why := conflict(e, c); why != "" {
			c.Skip = why
			res.Skipped = append(res.Skipped, c)
			continue
		}
		e.cands = append(e.cands, c)
		if !slices.Contains(e.op.Providers, c.Provider) {
			e.op.Providers = append(e.op.Providers, c.Provider)
		}
		if e.op.Justification == "" {
			e.op.Justification = c.Justification
		}
	}
	slices.SortStableFunc(entries, func(a, b *entry) int {
		return slices.Index(opOrder, a.op.Kind) - slices.Index(opOrder, b.op.Kind)
	})
	for _, e := range entries {
		slices.SortFunc(e.op.Providers, func(a, b string) int {
			return slices.Index(config.Providers, a) - slices.Index(config.Providers, b)
		})
	}
	kept, dropped := apply(entries, cfg, home)
	for _, e := range kept {
		res.Ops = append(res.Ops, e.op)
		res.Findings = append(res.Findings, e.cands...)
	}
	for _, d := range dropped {
		for _, c := range d.cands {
			c.Skip = d.why
			res.Skipped = append(res.Skipped, c)
		}
	}
	return res
}

// marketplacesFirst puts the plugins after everything else, so the
// marketplaces they come from are merged before they are looked at.
func marketplacesFirst(cands []agents.Candidate) []agents.Candidate {
	var rest, plugins []agents.Candidate
	for _, c := range cands {
		if c.Kind == agents.KindPlugin {
			plugins = append(plugins, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(rest, plugins...)
}

// noMarketplace says why a plugin has no marketplace for its provider: one
// declared there already, or one this proposal declares for it. A plugin
// installed in two providers whose marketplaces of one name differ is kept
// for the provider whose marketplace is declared, not dropped with the other.
func noMarketplace(c agents.Candidate, index map[string]*entry, cfg *config.Config) string {
	_, mk, _ := config.SplitPluginID(c.Name)
	if _, declared := cfg.Agents.Marketplaces[mk]; declared && slices.Contains(cfg.Agents.MarketplaceProviders(mk), c.Provider) {
		return ""
	}
	if e, ok := index[agents.KindMarketplace+"\x00"+mk]; ok && slices.Contains(e.op.Providers, c.Provider) {
		return ""
	}
	return fmt.Sprintf("its marketplace %q isn't declared for %s, and capture can't declare it there", mk, c.Provider)
}

var strictness = []string{config.PermissionDeny, config.PermissionReview, config.PermissionAllow}

// strictest keeps, for each command of a provider, only the strictest of the
// decisions found for it, since the agent applies that one. The others are
// returned as skipped.
func strictest(cands []agents.Candidate) (kept, dropped []agents.Candidate) {
	best := map[string]int{}
	rank := func(c agents.Candidate) int { return slices.Index(strictness, c.Decision) }
	key := func(c agents.Candidate) string { return c.Provider + "\x00" + c.Name }
	for _, c := range cands {
		if c.Kind == agents.KindPermission && c.Skip == "" {
			if r, seen := best[key(c)]; !seen || rank(c) < r {
				best[key(c)] = rank(c)
			}
		}
	}
	for _, c := range cands {
		if c.Kind == agents.KindPermission && c.Skip == "" && rank(c) > best[key(c)] {
			c.Skip = "the same command is also saved with a stricter decision, which is the one that applies"
			dropped = append(dropped, c)
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}

var opOrder = []string{proposal.AddMarketplace, proposal.AddPlugin, proposal.AddMCP, proposal.AddPermission}

func opOf(c agents.Candidate) proposal.Op {
	switch c.Kind {
	case agents.KindMarketplace:
		return proposal.Op{Kind: proposal.AddMarketplace, Name: c.Name, Source: c.Source}
	case agents.KindPlugin:
		return proposal.Op{Kind: proposal.AddPlugin, Name: c.Name}
	case agents.KindMCP:
		s := c.Server
		return proposal.Op{Kind: proposal.AddMCP, Name: c.Name, Server: &proposal.Server{
			Command: s.Command, Args: s.Args, URL: s.URL, EnvVars: s.EnvVars, BearerTokenEnv: s.BearerTokenEnv,
		}}
	}
	return proposal.Op{Kind: proposal.AddPermission, Decision: c.Decision, Command: c.Name, Justification: c.Justification}
}

// conflict says why c can't share the declaration e already makes for
// another provider, or "".
func conflict(e *entry, c agents.Candidate) string {
	first := e.cands[0]
	switch c.Kind {
	case agents.KindMarketplace:
		if !agents.SameSource(first.Source, c.Source) {
			return fmt.Sprintf("comes from %s, but %s's marketplace of that name comes from %s; declare one of them by hand", c.Source, first.Provider, first.Source)
		}
	case agents.KindMCP:
		if !sameServer(first.Server, c.Server) {
			return fmt.Sprintf("differs from %s's server of that name; declare them under different names by hand", first.Provider)
		}
	}
	return ""
}

func sameServer(a, b *agents.CapturedServer) bool {
	return a.Command == b.Command && slices.Equal(a.Args, b.Args) && a.URL == b.URL &&
		slices.Equal(slices.Sorted(slices.Values(a.EnvVars)), slices.Sorted(slices.Values(b.EnvVars))) &&
		a.BearerTokenEnv == b.BearerTokenEnv
}

type drop struct {
	cands []agents.Candidate
	why   string
}

// apply keeps the entries that edit cfg's files: each alone (a plugin along
// with the marketplaces kept before it), then all together, falling back to
// adding them one at a time if some pair clashes.
func apply(entries []*entry, cfg *config.Config, home string) (kept []*entry, dropped []drop) {
	try := func(ops []proposal.Op) error {
		_, err := proposal.Apply(ops, cfg, home)
		return err
	}
	var markets []proposal.Op
	for _, e := range entries {
		var with []proposal.Op
		if e.op.Kind != proposal.AddMarketplace {
			with = markets
		}
		if err := try(append(slices.Clone(with), e.op)); err != nil {
			dropped = append(dropped, drop{e.cands, reason(err, cfg.Path)})
			continue
		}
		if e.op.Kind == proposal.AddMarketplace {
			markets = append(markets, e.op)
		}
		kept = append(kept, e)
	}
	if len(kept) == 0 || try(ops(kept)) == nil {
		return kept, dropped
	}
	var all []*entry
	for _, e := range kept {
		if err := try(ops(append(slices.Clone(all), e))); err != nil {
			dropped = append(dropped, drop{e.cands, reason(err, cfg.Path)})
			continue
		}
		all = append(all, e)
	}
	return all, dropped
}

func ops(entries []*entry) []proposal.Op {
	out := make([]proposal.Op, len(entries))
	for i, e := range entries {
		out[i] = e.op
	}
	return out
}

// reason is an apply error without the "operation N (...)" it starts with
// and the config path it repeats.
func reason(err error, path string) string {
	if inner := errors.Unwrap(err); inner != nil {
		err = inner
	}
	text := strings.ReplaceAll(strings.ReplaceAll(err.Error(), path+": ", ""), "\n", "; ")
	return "can't be declared: " + text
}
