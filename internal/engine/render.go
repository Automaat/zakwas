package engine

import (
	"fmt"
	"io"
	"strings"
)

// Style controls how plans and apply progress are shown to people.
type Style struct {
	Color     bool
	ShowDiffs bool
}

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

func (s Style) paint(code, text string) string {
	if !s.Color || text == "" {
		return text
	}
	return code + text + reset
}

// Symbol is the one-character marker of an action, colored by kind.
func (s Style) Symbol(c Change) string {
	switch {
	case c.Destructive && c.Action == Run:
		return s.paint(bold+red, "▶")
	case c.Destructive:
		return s.paint(bold+red, string(c.Action))
	case c.Action == Create:
		return s.paint(green, "+")
	case c.Action == Update:
		return s.paint(yellow, "~")
	case c.Action == Remove:
		return s.paint(red, "-")
	case c.Action == Run:
		return s.paint(cyan, "▶")
	}
	return string(c.Action)
}

func (s Style) OK(text string) string   { return s.paint(green, text) }
func (s Style) Fail(text string) string { return s.paint(red, text) }
func (s Style) Dim(text string) string  { return s.paint(dim, text) }
func (s Style) Bold(text string) string { return s.paint(bold, text) }
func (s Style) Warn(text string) string { return s.paint(yellow, text) }

// maxTargetWidth caps the target column so one long path doesn't push every
// detail off screen.
const maxTargetWidth = 40

// Render writes the plan grouped by module, then the modules that are up to
// date, the destructive changes, and a one-line summary.
func Render(w io.Writer, p Plan, s Style) error {
	var b strings.Builder
	var upToDate []string
	for _, mp := range p {
		switch {
		case mp.Err != nil:
			fmt.Fprintf(&b, "%s %s: plan failed: %v\n", s.Fail("✗"), s.Bold(mp.Module), mp.Err)
			continue
		case len(mp.Changes) == 0:
			upToDate = append(upToDate, mp.Module)
			continue
		}
		b.WriteString(s.Bold(mp.Module) + "\n")
		width := 0
		for _, c := range mp.Changes {
			width = max(width, min(len([]rune(c.Target)), maxTargetWidth))
		}
		for _, c := range mp.Changes {
			line := "  " + s.Symbol(c) + " " + c.Target
			if sum := c.Summary(); sum != "" {
				pad := max(width-len([]rune(c.Target)), 0)
				line += strings.Repeat(" ", pad) + "  " + s.Dim(sum)
			}
			b.WriteString(line + "\n")
			if s.ShowDiffs && c.Diff != "" {
				s.writeDiff(&b, c.Diff)
			}
		}
	}
	if len(upToDate) > 0 {
		fmt.Fprintf(&b, "%s up to date: %s\n", s.OK("✓"), strings.Join(upToDate, ", "))
	}
	if d := destructive(p); len(d) > 0 {
		fmt.Fprintf(&b, "\n%s %s\n", s.Fail("⚠ destructive:"), strings.Join(d, ", "))
	}
	b.WriteString("\n" + summaryLine(p) + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func (s Style) writeDiff(b *strings.Builder, diff string) {
	for l := range strings.Lines(diff) {
		text := strings.TrimSuffix(l, "\n")
		switch {
		case strings.HasPrefix(text, "+") && !strings.HasPrefix(text, "+++"):
			text = s.paint(green, text)
		case strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "---"):
			text = s.paint(red, text)
		}
		b.WriteString("      " + text + "\n")
	}
}

// maxDestructive keeps the warning to one readable line.
const maxDestructive = 5

func destructive(p Plan) []string {
	var out []string
	for _, mp := range p {
		for _, c := range mp.Changes {
			if c.Destructive {
				out = append(out, c.Target)
			}
		}
	}
	if len(out) > maxDestructive {
		out = append(out[:maxDestructive], fmt.Sprintf("%d more", len(out)-maxDestructive))
	}
	return out
}

func summaryLine(p Plan) string {
	n := p.Counts()
	if n == (Counts{}) {
		if p.Err() != nil {
			return "No changes planned; some modules failed to plan."
		}
		return "No changes. The machine matches the config."
	}
	return fmt.Sprintf("Plan: %d to add, %d to change, %d to remove, %d to run.", n.Create, n.Update, n.Remove, n.Run)
}
