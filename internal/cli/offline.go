package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/engine"
	"github.com/Automaat/zakwas/internal/platform"
	"github.com/Automaat/zakwas/internal/runner"
)

// offlineChecked are the modules `check --offline` runs: they read only
// files, so they compare what zakwas owns with its recorded state without
// starting a command.
var offlineChecked = []string{"files", "links", "templates"}

// offlineSkipped are the modules `check --offline` never runs, because they
// ask a tool (brew, mise, an agent CLI, launchctl, defaults, sudo) or run commands.
var offlineSkipped = []string{"system", "brew", "mise", "agents", "commands", "launchd", "defaults"}

// errOffline is what a module gets when it tries to start a command under
// --offline, so a module that wrongly joins the offline set fails loudly.
var errOffline = errors.New("--offline starts no commands")

type offlineRunner struct{}

func (offlineRunner) Run(context.Context, runner.Cmd) (runner.Result, error) {
	return runner.Result{}, errOffline
}

func (offlineRunner) Installed(string) bool { return false }

// offlineModules keeps the modules that run offline. A module named with
// --only that can't run offline is an error; without --only it is skipped.
func offlineModules(mods []engine.Module, explicit bool) ([]engine.Module, error) {
	var out []engine.Module
	for _, m := range mods {
		switch {
		case slices.Contains(offlineChecked, m.Name()):
			out = append(out, m)
		case explicit:
			return nil, fmt.Errorf("module %q can't run with -offline: it needs external commands", m.Name())
		}
	}
	return out, nil
}

// offlineSkippedOn is offlineSkipped without the modules goos does not have.
func offlineSkippedOn(goos string) []string {
	return slices.DeleteFunc(slices.Clone(offlineSkipped), func(name string) bool { return !platform.Supported(name, goos) })
}

// offlineNotChecked lists what an offline check left out: the modules that
// need commands, then the offline ones --only did not select.
func offlineNotChecked(ran []engine.Module, goos string) []string {
	out := offlineSkippedOn(goos)
	for _, name := range offlineChecked {
		if !platform.Supported(name, goos) {
			continue
		}
		if !slices.ContainsFunc(ran, func(m engine.Module) bool { return m.Name() == name }) {
			out = append(out, name)
		}
	}
	return out
}

// offlineNote tells the reader what an offline check did not look at, so a
// clean result isn't taken for a full check.
func offlineNote(style engine.Style, notChecked []string) string {
	return style.Dim("offline: not checked: "+strings.Join(notChecked, ", ")+"; run `zakwas check` for those") + "\n"
}
