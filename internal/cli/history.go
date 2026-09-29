package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/runner"
)

// HistoryPath is the append-only log of applies. Brew zap and mise prune
// can't be undone, so this is the record of what ran from which commit.
func HistoryPath(home string) string {
	return filepath.Join(home, ".local", "state", "zakwas", "history.jsonl")
}

type historyEntry struct {
	FormatVersion string              `json:"format_version"`
	Time          time.Time           `json:"time"`
	Host          string              `json:"host,omitempty"`
	Commit        string              `json:"commit,omitempty"`
	Dirty         bool                `json:"dirty,omitempty"`
	Changes       []engine.JSONChange `json:"changes"`
	Result        *resultJSON         `json:"result"`
	Error         string              `json:"error,omitempty"`
}

func git(ctx context.Context, r runner.Runner, root string, args ...string) (string, bool) {
	out, err := runner.Output(ctx, r, runner.Cmd{Name: "git", Args: append([]string{"-C", root}, args...)})
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// repoWarnings flags applying from a checkout that isn't the reviewed state
// of main. They are warnings, not errors: testing a branch is legitimate.
// "Behind" is relative to the last fetch; zakwas doesn't hit the network here.
func repoWarnings(ctx context.Context, r runner.Runner, root string) []string {
	var warnings []string
	if status, ok := git(ctx, r, root, "status", "--porcelain"); ok && status != "" {
		warnings = append(warnings, "applying uncommitted changes from "+root)
	}
	if branch, ok := git(ctx, r, root, "rev-parse", "--abbrev-ref", "HEAD"); ok && branch != "main" {
		warnings = append(warnings, "applying from branch "+branch+", not main")
	}
	if behind, ok := git(ctx, r, root, "rev-list", "--count", "HEAD..@{upstream}"); ok {
		if n, err := strconv.Atoi(behind); err == nil && n > 0 {
			warnings = append(warnings, behind+" commit(s) behind upstream; git pull first?")
		}
	}
	return warnings
}

// recordHistory ignores cancellation, so an interrupted apply still records
// its commit.
func recordHistory(ctx context.Context, env Env, root string, plan engine.Plan, result engine.Result, applyErr error) error {
	ctx = context.WithoutCancel(ctx)
	entry := historyEntry{
		FormatVersion: engine.FormatVersion, Time: env.now(), Host: env.Host,
		Changes: []engine.JSONChange{}, Result: newResultJSON(result),
	}
	entry.Commit, _ = git(ctx, env.Runner, root, "rev-parse", "HEAD")
	if status, ok := git(ctx, env.Runner, root, "status", "--porcelain"); ok {
		entry.Dirty = status != ""
	}
	for _, mp := range plan {
		for _, c := range mp.Changes {
			j := c.JSON(false)
			j.Module = mp.Module
			entry.Changes = append(entry.Changes, j)
		}
	}
	if applyErr != nil {
		entry.Error = applyErr.Error()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := HistoryPath(env.Home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
