package cli

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Automaat/zakwas/internal/runner"
)

type operationPhaseKey struct{}

func withOperationPhase(ctx context.Context, phase string) context.Context {
	return context.WithValue(ctx, operationPhaseKey{}, phase)
}

type operationTiming struct {
	name, result string
	duration     time.Duration
}

type operationProgress struct {
	mu       sync.Mutex
	out      *console
	interval time.Duration
	timings  []operationTiming
}

func (p *operationProgress) line(name, result string, elapsed time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.out.printf("%s: %s (%s)\n", name, result, round(elapsed))
}

func (p *operationProgress) summary() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.out.print("Upgrade refresh and planning timings (slowest first):\n")
	if len(p.timings) == 0 {
		p.out.print("  No external operations.\n")
		return
	}
	slices.SortStableFunc(p.timings, func(a, b operationTiming) int {
		return cmp.Compare(b.duration, a.duration)
	})
	for _, timing := range p.timings {
		p.out.printf("  %s: %s (%s)\n", timing.name, timing.result, round(timing.duration))
	}
}

type operationRunner struct {
	runner.Runner
	progress *operationProgress
}

func (r *operationRunner) Run(ctx context.Context, cmd runner.Cmd) (runner.Result, error) {
	phase, _ := ctx.Value(operationPhaseKey{}).(string)
	if phase == "" {
		return r.Runner.Run(ctx, cmd)
	}
	name := plainOperation(strings.TrimSpace(phase+" "+runner.Operation(ctx)) + ": " + operationCommand(cmd))
	started := time.Now()
	r.progress.line(name, "started", 0)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(r.progress.interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				r.progress.line(name, "running", time.Since(started))
			}
		}
	}()
	res, err := r.Runner.Run(ctx, cmd)
	close(stop)
	<-stopped
	elapsed := time.Since(started)
	result := "done"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), ctx.Err() != nil:
		result = "cancelled"
	case err != nil, res.ExitCode != 0:
		result = "failed"
	}
	r.progress.line(name, result, elapsed)
	r.progress.mu.Lock()
	r.progress.timings = append(r.progress.timings, operationTiming{name, result, elapsed})
	r.progress.mu.Unlock()
	return res, err
}

func operationCommand(cmd runner.Cmd) string {
	// Shell snippets and arbitrary command arguments can contain secrets.
	name := cmd.Name
	switch cmd.Name {
	case "claude", "codex", "copilot", "brew", "mise":
		name = strings.Join(append([]string{cmd.Name}, cmd.Args...), " ")
	case "git":
		for _, arg := range cmd.Args {
			if !strings.HasPrefix(arg, "-") {
				name = strings.Join([]string{name, arg}, " ")
				break
			}
		}
	}
	return name
}

func plainOperation(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
}
