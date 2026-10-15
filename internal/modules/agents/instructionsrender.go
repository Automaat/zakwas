package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/templates"
)

// sharedInstructions names the optional shared target, ~/.agents/AGENTS.md,
// both as the template's provider and as the plan's group.
const sharedInstructions = "shared"

// instructionsLimits are the sizes, in bytes, past which a provider
// doesn't read its global instructions: Codex stops at 32768 bytes of
// AGENTS.md.
var instructionsLimits = map[string]int{config.ProviderCodex: 32768}

// importHops is how many files deep Claude follows @path imports.
const importHops = 4

// instructionData is what an instructions file can reference.
type instructionData struct {
	Home     string
	Vars     map[string]string
	Provider string
}

// renderInstructions renders agents.instructions for provider: each file as
// a template, its @path imports kept for Claude and inlined for the rest,
// the results joined in order. A part that renders empty is left out, so a
// file can be meant for some providers only.
func (m *Module) renderInstructions(provider string) ([]byte, error) {
	data := instructionData{Home: m.Paths.Home, Vars: m.Vars, Provider: provider}
	var parts []string
	for _, file := range m.Agents.Instructions {
		src := filepath.Clean(m.Paths.Src(file))
		raw, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", m.Paths.Pretty(src), pathErr(err))
		}
		text, err := templates.RenderText(filepath.Base(src), string(raw), data, nil)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", m.Paths.Pretty(src), err)
		}
		im := importer{paths: m.Paths, inline: provider != config.ProviderClaude}
		out, err := im.expand(string(text), filepath.Dir(src), []string{src})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.Paths.Pretty(src), err)
		}
		if strings.TrimSpace(out) != "" {
			parts = append(parts, out)
		}
	}
	var out []byte
	switch len(parts) {
	case 0:
	case 1:
		out = []byte(parts[0])
	default:
		for i := range parts {
			parts[i] = strings.TrimRight(parts[i], "\n")
		}
		out = []byte(strings.Join(parts, "\n\n") + "\n")
	}
	if limit, ok := instructionsLimits[provider]; ok && len(out) > limit {
		return nil, fmt.Errorf("rendered instructions are %d bytes, over %s's %d byte limit", len(out), provider, limit)
	}
	return out, nil
}

// pathErr drops the path from a *fs.PathError, which the caller names.
func pathErr(err error) error {
	if pe := (*fs.PathError)(nil); errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// importRef finds an @path import: at the start of a line or after
// whitespace, like Claude Code reads them.
var importRef = regexp.MustCompile(`(^|\s)@(\S+)`)

// errNotAnImport marks an @path that names no file and doesn't look like an
// import for sure, such as @types/node in prose.
var errNotAnImport = errors.New("not an import")

// certainImport reports whether a path can only be meant as an import: it
// ends in ".md" or starts like a file path. Others, such as @types/node,
// are imports only when the file exists.
func certainImport(path string) bool {
	return strings.HasSuffix(path, ".md") || strings.HasPrefix(path, "./") ||
		strings.HasPrefix(path, "../") || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "/")
}

// importer handles the @path imports of instructions text. Only a path with
// a "/" or ending in ".md" counts, so mentions like @claude are left alone;
// one that names no file counts only when it is certainly a path.
// Imports in code blocks and code spans are not imports, as in Claude Code.
// With inline set, an import is replaced by the imported file as is, not
// rendered as a template; otherwise it stays, a relative one of the
// instructions file made absolute, since the rendered copy lives somewhere
// else than its source. Imports are checked either way, down to the files
// Claude Code would follow.
type importer struct {
	paths  config.Paths
	inline bool
}

// expand processes text found in the file at the top of stack, whose
// directory is dir. stack holds the files being expanded, outermost first,
// to catch cycles and imports nested too deep.
func (im importer) expand(text, dir string, stack []string) (string, error) {
	var out strings.Builder
	var errs []error
	fenced := false
	for line := range strings.SplitAfterSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			out.WriteString(line)
			continue
		}
		if fenced {
			out.WriteString(line)
			continue
		}
		spans := strings.Split(line, "`")
		for i := 0; i < len(spans); i += 2 {
			spans[i] = importRef.ReplaceAllStringFunc(spans[i], func(match string) string {
				groups := importRef.FindStringSubmatch(match)
				lead, ref := groups[1], groups[2]
				path := strings.TrimRight(ref, ".,;:!?)]}>\"'")
				if !strings.Contains(path, "/") && !strings.HasSuffix(path, ".md") {
					return match
				}
				replaced, err := im.resolve(path, dir, stack)
				if errors.Is(err, errNotAnImport) {
					return match
				}
				if err != nil {
					errs = append(errs, fmt.Errorf("import @%s: %w", path, err))
					return match
				}
				return lead + replaced + ref[len(path):]
			})
		}
		out.WriteString(strings.Join(spans, "`"))
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	return out.String(), nil
}

// resolve returns what an import of path becomes: the imported text when
// inlining, else the import itself.
func (im importer) resolve(path, dir string, stack []string) (string, error) {
	target := im.paths.Dst(path)
	relative := !filepath.IsAbs(target)
	if relative {
		target = filepath.Join(dir, target)
	}
	target = filepath.Clean(target)
	if len(stack) > importHops {
		return "", fmt.Errorf("imports nest more than %d files deep", importHops)
	}
	if slices.Contains(stack, target) {
		return "", fmt.Errorf("%s imports itself through %s", im.paths.Pretty(target), im.paths.Pretty(stack[len(stack)-1]))
	}
	raw, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) && !certainImport(path) {
		return "", errNotAnImport
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", im.paths.Pretty(target), pathErr(err))
	}
	if !im.inline && relative && len(stack) == 1 && strings.ContainsAny(target, " \t") {
		return "", fmt.Errorf("%s: path has whitespace, which an import can't hold; move it or import it with ~/", im.paths.Pretty(target))
	}
	nested, err := im.expand(string(raw), filepath.Dir(target), append(slices.Clone(stack), target))
	if err != nil {
		return "", fmt.Errorf("%s: %w", im.paths.Pretty(target), err)
	}
	switch {
	case im.inline:
		return strings.TrimRight(nested, "\n"), nil
	case relative && len(stack) == 1:
		return "@" + target, nil
	}
	return "@" + path, nil
}
