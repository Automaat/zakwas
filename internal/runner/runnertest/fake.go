// Package runnertest provides a scripted Runner for tests.
package runnertest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Automaat/zakwas/internal/runner"
)

// Fake answers commands from a table keyed by the rendered command line.
// Unknown commands fail loudly so tests can't silently miss a call.
type Fake struct {
	mu        sync.Mutex
	responses map[string][]runner.Result
	missing   map[string]bool
	Calls     []runner.Cmd
}

func New() *Fake {
	return &Fake{responses: map[string][]runner.Result{}, missing: map[string]bool{}}
}

// Missing makes Installed report the binaries as not installed; every other
// binary is installed.
func (f *Fake) Missing(names ...string) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range names {
		f.missing[n] = true
	}
	return f
}

// Install makes Installed find a binary again, as an install step would.
func (f *Fake) Install(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.missing, name)
}

func (f *Fake) Installed(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.missing[name]
}

// On registers a response for a command line. Registering the same line
// several times queues responses; the last one repeats.
func (f *Fake) On(cmdline string, res runner.Result) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[cmdline] = append(f.responses[cmdline], res)
	return f
}

// OnOK registers a successful response with the given stdout.
func (f *Fake) OnOK(cmdline, stdout string) *Fake {
	return f.On(cmdline, runner.Result{Stdout: stdout})
}

func (f *Fake) Run(_ context.Context, c runner.Cmd) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
	key := c.String()
	queue, ok := f.responses[key]
	if !ok {
		return runner.Result{}, fmt.Errorf("runnertest: unexpected command %q", key)
	}
	res := queue[0]
	if len(queue) > 1 {
		f.responses[key] = queue[1:]
	}
	return res, nil
}

// Lines returns every executed command line, in order.
func (f *Fake) Lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = c.String()
	}
	return out
}

// Ran reports whether a command line starting with prefix was executed.
func (f *Fake) Ran(prefix string) bool {
	for _, l := range f.Lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
