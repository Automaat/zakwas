package cli

import (
	"fmt"
	"time"

	"github.com/Automaat/zakwas/internal/engine"
)

// progress prints `[n/total] module ± target` per step. Quiet steps finish on
// the same line; a step that streams a tool's output gets its own header and
// a closing result line, folded into a group on GitHub Actions.
type progress struct {
	out    *console
	style  engine.Style
	groups bool
}

func (p *progress) header(s engine.Step) string {
	counter := p.style.Dim(fmt.Sprintf("[%d/%d]", s.N, s.Total))
	return fmt.Sprintf("%s %s %s %s", counter, s.Module, p.style.Symbol(s.Change), s.Change.Target)
}

func (p *progress) Start(s engine.Step) {
	if !s.Change.Streams {
		p.out.print(p.header(s) + " ")
		return
	}
	if p.groups {
		p.out.print("::group::")
	}
	p.out.print(p.header(s) + "\n")
}

func (p *progress) Done(s engine.Step, d time.Duration, err error) {
	result := p.style.OK("✓") + " " + p.style.Dim(round(d))
	if err != nil {
		result = p.style.Fail("✗ failed")
	}
	if !s.Change.Streams {
		p.out.print(result + "\n")
		return
	}
	if p.groups {
		p.out.print("::endgroup::\n")
	}
	p.out.print(p.header(s) + " " + result + "\n")
}

func round(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}

func recap(r engine.Result, style engine.Style) string {
	total := r.Applied + r.Failed + r.Skipped
	line := fmt.Sprintf("Applied %d of %d steps in %s.", r.Applied, total, round(r.Duration))
	if r.Failed > 0 || r.Skipped > 0 {
		line += " " + style.Fail(fmt.Sprintf("%d failed, %d skipped.", r.Failed, r.Skipped))
	}
	return line
}
