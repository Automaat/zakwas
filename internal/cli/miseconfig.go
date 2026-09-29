package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/runner"
)

var (
	tomlHeader  = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	tomlKeyLine = regexp.MustCompile(`^(\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[A-Za-z0-9_-]+)(\s*=\s*)(.*)$`)
	tomlString  = regexp.MustCompile(`^("(?:[^"\\]|\\.)*"|'[^']*')`)
	tomlArray   = regexp.MustCompile(`^\[(?:[^\]"']|"(?:[^"\\]|\\.)*"|'[^']*')*\]`)
	tomlVersion = regexp.MustCompile(`(\bversion\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*')`)
)

type miseTool struct {
	Version string `json:"version"`
	Source  struct {
		Path string `json:"path"`
	} `json:"source"`
}

// globalMiseConfig copies the user's global mise config with every active
// tool pinned to the exact version in use, so the new repo reproduces this
// machine rather than "latest". Tool options and other sections ([settings],
// [env], …) are kept as written.
func globalMiseConfig(ctx context.Context, r runner.Runner, home string) ([]byte, error) {
	ls := runner.Cmd{Name: "mise", Args: []string{"ls", "--global", "--current", "--json"}, Dir: mise.Dir}
	stdout, err := runner.Output(ctx, r, ls)
	if err != nil {
		return nil, err
	}
	var tools map[string][]miseTool
	if err := json.Unmarshal([]byte(stdout), &tools); err != nil {
		return nil, fmt.Errorf("%s: %w", ls, err)
	}
	pins := map[string][]string{}
	sources := map[string]int{}
	for name, versions := range tools {
		for _, t := range versions {
			if v := strconv.Quote(t.Version); t.Version != "" && !slices.Contains(pins[name], v) {
				pins[name] = append(pins[name], v)
			}
			if strings.HasSuffix(t.Source.Path, ".toml") {
				sources[t.Source.Path]++
			}
		}
	}
	base, err := os.ReadFile(globalConfigPath(sources, home))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return []byte(pinTools(string(base), pins)), nil
}

// globalConfigPath picks the file most global tools come from, falling back
// to the config mise reads by default.
func globalConfigPath(sources map[string]int, home string) string {
	best := ""
	for _, path := range slices.Sorted(maps.Keys(sources)) {
		if best == "" || sources[path] > sources[best] {
			best = path
		}
	}
	if best != "" {
		return best
	}
	if env := os.Getenv("MISE_GLOBAL_CONFIG_FILE"); env != "" {
		return env
	}
	return config.Paths{Home: home}.Dst(mise.InstalledConfig)
}

// pinTools rewrites only tool versions in a mise config: `tool = "x"`,
// arrays, the version of inline tables and of [tools.<name>] tables. Tools
// mise reports but the file doesn't list are added under [tools].
func pinTools(data string, pins map[string][]string) string {
	lines := strings.SplitAfter(data, "\n")
	section, toolsHeader := "", -1
	seen := map[string]bool{}
	for i, line := range lines {
		body, hasNL := strings.CutSuffix(line, "\n")
		nl := ""
		if hasNL {
			nl = "\n"
		}
		if m := tomlHeader.FindStringSubmatch(body); m != nil {
			section = m[1]
			if section == "tools" {
				toolsHeader = i
			}
			continue
		}
		m := tomlKeyLine.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		key := unquoteKey(m[2])
		name := key
		switch {
		case section == "tools":
		case strings.HasPrefix(section, "tools.") && key == "version":
			name = unquoteKey(strings.TrimSpace(strings.TrimPrefix(section, "tools.")))
		default:
			continue
		}
		versions, ok := pins[name]
		if !ok {
			continue
		}
		if value, ok := pinValue(m[4], versions); ok {
			lines[i] = m[1] + m[2] + m[3] + value + nl
			seen[name] = true
		}
	}
	var missing []string
	for _, name := range slices.Sorted(maps.Keys(pins)) {
		if !seen[name] {
			missing = append(missing, tomlKey(name)+" = "+pinned(pins[name])+"\n")
		}
	}
	if len(missing) == 0 {
		return strings.Join(lines, "")
	}
	if toolsHeader >= 0 {
		lines = slices.Insert(lines, toolsHeader+1, missing...)
		return strings.Join(lines, "")
	}
	out := strings.Join(lines, "")
	switch {
	case out == "":
	case strings.HasSuffix(out, "\n"):
		out += "\n"
	default:
		out += "\n\n"
	}
	return out + "[tools]\n" + strings.Join(missing, "")
}

// pinValue swaps the version in a TOML value, keeping what follows it (a
// comment) and, for inline tables, every other option.
func pinValue(value string, versions []string) (string, bool) {
	switch {
	case strings.HasPrefix(value, "{"):
		loc := tomlVersion.FindStringSubmatchIndex(value)
		if loc == nil {
			return "", false
		}
		return value[:loc[4]] + versions[0] + value[loc[5]:], true
	case strings.HasPrefix(value, "["):
		if m := tomlArray.FindString(value); m != "" {
			return pinned(versions) + value[len(m):], true
		}
	default:
		if m := tomlString.FindString(value); m != "" {
			return pinned(versions) + value[len(m):], true
		}
	}
	return "", false
}

func pinned(versions []string) string {
	if len(versions) == 1 {
		return versions[0]
	}
	return "[" + strings.Join(versions, ", ") + "]"
}

func unquoteKey(k string) string {
	if strings.HasPrefix(k, "'") && strings.HasSuffix(k, "'") && len(k) >= 2 {
		return k[1 : len(k)-1]
	}
	if u, err := strconv.Unquote(k); err == nil && strings.HasPrefix(k, `"`) {
		return u
	}
	return k
}

func tomlKey(name string) string {
	if bareKey.MatchString(name) {
		return name
	}
	return strconv.Quote(name)
}
