package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/proposal"
	"github.com/Automaat/zakwas/internal/runner"
)

func runApprove(ctx context.Context, env Env, args []string, out, errOut *console) int {
	fs, o := newConfigFlags("approve", approveUsage, false, errOut)
	pos, code, ok := parseSub(fs, out, args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		fs.Usage()
		return ExitUsage
	}
	cfg, err := loadConfig(o.cfgPath, env.Cwd, env.Home)
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if env.AgentSession != "" && cfg.Agents.GuardEnabled() {
		errOut.printf("zakwas: refusing to approve from a %s session: an agent that can approve a proposal can change its own setup.\n"+
			"Run `zakwas approve %s` in a terminal outside the agent, or set agents.guard: false in %s\n", env.AgentSession, pos[0], cfg.Path)
		return ExitErr
	}
	if !env.StdinTTY {
		errOut.print("zakwas: stdin is not a terminal, so approve can't ask for confirmation; run `zakwas approve " + pos[0] + "` in a terminal\n")
		return ExitUsage
	}
	store := proposal.Store{Dir: proposal.Dir(env.Home)}
	p, err := store.Load(pos[0])
	if err != nil {
		errOut.fail(err)
		return ExitErr
	}
	if err := checkProposal(p, cfg, env); err != nil {
		errOut.fail(err)
		return ExitErr
	}
	held, code, ok := acquireLock(ctx, env, "approve", false, errOut)
	if !ok {
		return code
	}
	defer func() { _ = held.Release() }()

	ap := &approval{env: env, cfg: cfg, p: p, out: out, errOut: errOut}
	return ap.run(ctx, store)
}

// checkProposal refuses a proposal made for another config or too old.
func checkProposal(p *proposal.Proposal, cfg *config.Config, env Env) error {
	switch {
	case p.Config != cfg.Path:
		return fmt.Errorf("proposal %s was made for %s, not %s", p.ID, p.Config, cfg.Path)
	case p.Expired(env.now()):
		return fmt.Errorf("proposal %s expired on %s (proposals last %d days); propose the change again", p.ID, p.Expires().Format("2006-01-02"), int(proposal.MaxAge.Hours()/24))
	}
	return nil
}

// approval applies one proposal. ask shows the reviewed proposal and says
// whether to go on; nil asks on the terminal. observer reports the steps
// of the apply; nil prints them to out. maxTier, when set, refuses a
// proposal rated above it. outcome says why nothing was applied when that
// was on purpose. via says where it was approved, for the commit message,
// and applied turns true once the changes ran and the commit is next.
type approval struct {
	env      Env
	cfg      *config.Config
	p        *proposal.Proposal
	out      *console
	errOut   *console
	ask      func(ctx context.Context, r *review) (bool, error)
	observer func(style engine.Style) engine.Observer
	maxTier  string
	outcome  string
	via      string
	applied  bool

	summary, tier string
}

// Why an approval applied nothing.
const (
	outcomeDeclined = "declined"
	outcomeRefused  = "refused"
)

// review is a proposal as shown to whoever approves it.
type review struct {
	ev     *evaluation
	rels   []string
	branch string
	style  engine.Style
}

func (a *approval) fail(err error) int {
	a.errOut.fail(err)
	return ExitErr
}

func (a *approval) run(ctx context.Context, store proposal.Store) int {
	rels := make([]string, 0, len(a.p.Base))
	for rel, want := range a.p.Base {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return a.fail(fmt.Errorf("proposal %s names %q, which is outside the config repo", a.p.ID, rel))
		}
		data, err := os.ReadFile(filepath.Join(a.cfg.Root, filepath.FromSlash(rel)))
		if err != nil {
			return a.fail(err)
		}
		if proposal.Hash(data) != want {
			return a.fail(fmt.Errorf("%s changed since proposal %s was made; propose the change again", rel, a.p.ID))
		}
		rels = append(rels, rel)
	}
	slices.Sort(rels)
	cfgRel, err := filepath.Rel(a.cfg.Root, a.cfg.Path)
	if err != nil {
		return a.fail(err)
	}
	checked := slices.Clone(rels)
	if cfgRel = filepath.ToSlash(cfgRel); !slices.Contains(checked, cfgRel) {
		checked = append([]string{cfgRel}, checked...)
	}
	branch, err := a.checkGit(ctx, checked)
	if err != nil {
		return a.fail(err)
	}
	ev, err := evaluate(ctx, a.env, a.cfg, a.p.Ops)
	if err != nil {
		return a.fail(err)
	}
	editRels := make([]string, len(ev.edits))
	for i, e := range ev.edits {
		editRels[i] = e.Rel
	}
	slices.Sort(editRels)
	if !slices.Equal(editRels, rels) {
		return a.fail(fmt.Errorf("proposal %s edits %s, which isn't what it was made against; propose the change again", a.p.ID, strings.Join(editRels, ", ")))
	}
	if err := ev.plan.Err(); err != nil {
		return a.fail(fmt.Errorf("can't apply: %w", err))
	}

	style := engine.Style{Color: a.env.Color, ShowDiffs: true}
	a.summary, a.tier = proposal.Summaries(ev.ops), ev.tier
	if a.maxTier == proposal.TierLow && ev.tier != proposal.TierLow {
		a.outcome = outcomeRefused
		return a.fail(fmt.Errorf("proposal %s is %s risk, and only a terminal applies those: run `zakwas approve %s`", a.p.ID, ev.tier, a.p.ID))
	}
	r := &review{ev: ev, rels: rels, branch: branch, style: style}
	ask := a.ask
	if ask == nil {
		ask = a.askTerminal
	}
	yes, err := ask(ctx, r)
	switch {
	case err != nil:
		a.outcome = outcomeDeclined
		return a.fail(fmt.Errorf("couldn't get a confirmation, so nothing was applied: %w", err))
	case !yes:
		a.outcome = outcomeDeclined
		a.out.print("aborted\n")
		return ExitErr
	}

	if _, err := a.checkGit(ctx, checked); err != nil {
		return a.fail(fmt.Errorf("while waiting for the answer: %w", err))
	}
	restore, err := writeEdits(ev.edits)
	if err != nil {
		return a.fail(err)
	}
	if code := a.apply(ctx, ev, style); code != ExitOK {
		if err := restore(); err != nil {
			a.errOut.fail(fmt.Errorf("couldn't restore every config file; check them with git status: %w", err))
		} else {
			a.errOut.print("zakwas: restored the config files; the proposal is still pending\n")
		}
		return code
	}
	a.applied = true
	if err := store.Delete(a.p.ID); err != nil {
		a.errOut.print("zakwas: warning: proposal " + a.p.ID + " not removed: " + err.Error() + "\n")
	}
	if err := a.commit(ctx, rels); err != nil {
		a.errOut.fail(fmt.Errorf("applied, but the commit failed; commit %s yourself: %w", strings.Join(rels, ", "), err))
		return ExitErr
	}
	sha, _ := git(ctx, a.env.Runner, a.cfg.Root, "rev-parse", "--short", "HEAD")
	a.out.printf("Committed %s on %s; not pushed.\n", sha, branch)
	return ExitOK
}

// showReview prints what the approver is asked to confirm: the proposal,
// why it is rated as it is, the diff and the plan.
func (a *approval) showReview(out *console, r *review) error {
	out.printf("Proposal %s: %s (%s risk)\n", a.p.ID, a.summary, a.tier)
	for _, reason := range r.ev.reasons {
		out.print("  " + reason + "\n")
	}
	out.print("\n" + r.ev.diff + "\n\n")
	return engine.Render(out, r.ev.plan, r.style)
}

func (a *approval) question(r *review) string {
	return fmt.Sprintf("Approve %s: apply %d step(s) and commit %s to %s?", a.p.ID, r.ev.plan.Steps(), strings.Join(r.rels, ", "), r.branch)
}

// askTerminal shows the review and reads y or yes from stdin.
func (a *approval) askTerminal(_ context.Context, r *review) (bool, error) {
	if err := a.showReview(a.out, r); err != nil {
		return false, err
	}
	a.out.print("\n" + a.question(r) + " [y/N] ")
	line, _ := bufio.NewReader(a.env.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// checkGit requires the edited files to be tracked and clean in a git
// checkout on a branch, since approve commits them, and returns the branch.
func (a *approval) checkGit(ctx context.Context, rels []string) (string, error) {
	root := a.cfg.Root
	if _, ok := git(ctx, a.env.Runner, root, "rev-parse", "--git-dir"); !ok {
		return "", fmt.Errorf("%s is not a git repository; approve commits the edit", root)
	}
	branch, ok := git(ctx, a.env.Runner, root, "symbolic-ref", "--short", "-q", "HEAD")
	if !ok || branch == "" {
		return "", errors.New("the config repo is on a detached HEAD; check out a branch first")
	}
	for _, rel := range rels {
		if _, ignored := git(ctx, a.env.Runner, root, "check-ignore", "-q", "--", rel); ignored {
			return "", fmt.Errorf("%s is ignored by git, so approve couldn't commit it", rel)
		}
	}
	status, ok := git(ctx, a.env.Runner, root, append([]string{"status", "--porcelain", "--"}, rels...)...)
	switch {
	case !ok:
		return "", errors.New("can't read the git status of the config repo")
	case status != "":
		return "", fmt.Errorf("%s has uncommitted changes; commit or stash them first:\n%s", strings.Join(rels, ", "), status)
	}
	return branch, nil
}

// apply writes the plan for the edited config and runs it. The edited files
// are already on disk, so the plan is made again and must match the one that
// was reviewed.
func (a *approval) apply(ctx context.Context, ev *evaluation, style engine.Style) int {
	cfg, err := config.Load(a.cfg.Path, a.env.Home)
	if err != nil {
		a.errOut.fail(err)
		return ExitErr
	}
	var plan engine.Plan
	if len(ev.modules) > 0 {
		mods, err := selectModules(Modules(cfg, a.env), strings.Join(ev.modules, ","))
		if err != nil {
			a.errOut.fail(err)
			return ExitErr
		}
		plan = engine.Build(ctx, mods)
	}
	if err := plan.Err(); err != nil {
		a.errOut.fail(err)
		return ExitErr
	}
	if !reflect.DeepEqual(plan.JSONModules(true), ev.plan.JSONModules(true)) {
		a.errOut.print("zakwas: the plan changed after review; nothing was applied\n")
		return ExitErr
	}
	a.out.print("\n")
	var obs engine.Observer = &progress{out: a.out, style: style, groups: a.env.GitHubActions}
	if a.observer != nil {
		obs = a.observer(style)
	}
	result, applyErr := engine.Apply(ctx, obs, plan)
	if err := recordHistory(ctx, a.env, cfg.Root, plan, result, applyErr); err != nil {
		a.errOut.print("zakwas: warning: history not recorded: " + err.Error() + "\n")
	}
	a.out.print("\n" + recap(result, style) + "\n")
	if applyErr != nil {
		a.errOut.fail(applyErr)
		return ExitErr
	}
	return ExitOK
}

func (a *approval) commit(ctx context.Context, rels []string) error {
	root := a.cfg.Root
	subject := "zakwas: " + a.summary
	if r := []rune(subject); len(r) > 72 {
		subject = string(r[:71]) + "…"
	}
	via := a.via
	if via == "" {
		via = "a terminal"
	}
	body := fmt.Sprintf("Proposal %s (%s risk), approved in %s.", a.p.ID, a.tier, via)
	run := func(args ...string) error {
		return runner.Check(ctx, a.env.Runner, runner.Cmd{Name: "git", Args: append([]string{"-C", root}, args...)})
	}
	if err := run(append([]string{"add", "--"}, rels...)...); err != nil {
		return err
	}
	return run(append([]string{"commit", "-m", subject, "-m", body, "--"}, rels...)...)
}

// writeEdits replaces the files with their new contents and returns a func
// that puts the old contents back. Each write first checks the file still
// holds what the edit was made from, so a change saved since the prompt
// isn't overwritten; the restore does the same against the new contents.
func writeEdits(edits []proposal.Edit) (restore func() error, err error) {
	var done []proposal.Edit
	restore = func() error {
		var errs []error
		for _, e := range slices.Backward(done) {
			errs = append(errs, replaceFile(e.Path, e.New, e.Old))
		}
		return errors.Join(errs...)
	}
	for _, e := range edits {
		if err := replaceFile(e.Path, e.Old, e.New); err != nil {
			return func() error { return nil }, errors.Join(err, restore())
		}
		done = append(done, e)
	}
	return restore, nil
}

// replaceFile writes data through a temporary file and a rename, keeping the
// file's mode, but only while the file still holds want.
func replaceFile(path string, want, data []byte) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, want) {
		return fmt.Errorf("%s changed while zakwas was working on it; nothing was overwritten", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".zakwas-edit-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	merr := os.Chmod(tmp.Name(), info.Mode().Perm())
	if err := errors.Join(werr, cerr, merr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
