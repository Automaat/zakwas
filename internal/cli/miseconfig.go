package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/modules/mise"
	"github.com/Automaat/zakwas/internal/runner"
)

type miseTool struct {
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version"`
	Installed        bool   `json:"installed"`
	Source           struct {
		Path string `json:"path"`
	} `json:"source"`
}

// miseTools lists the active global tools, as mise resolves them.
func miseTools(ctx context.Context, r runner.Runner) (map[string][]miseTool, error) {
	ls := runner.Cmd{Name: "mise", Args: []string{"ls", "--global", "--current", "--json"}, Dir: mise.Dir}
	stdout, err := runner.Output(ctx, r, ls)
	if err != nil {
		return nil, err
	}
	var tools map[string][]miseTool
	if err := json.Unmarshal([]byte(stdout), &tools); err != nil {
		return nil, fmt.Errorf("%s: %w", ls, err)
	}
	return tools, nil
}

// globalConfigPath picks the file most global tools come from, falling back
// to the config mise reads by default.
func globalConfigPath(tools map[string][]miseTool, home string) string {
	count := map[string]int{}
	for _, versions := range tools {
		for _, t := range versions {
			if strings.HasSuffix(t.Source.Path, ".toml") {
				count[t.Source.Path]++
			}
		}
	}
	best := ""
	for _, path := range slices.Sorted(maps.Keys(count)) {
		if best == "" || count[path] > count[best] {
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

// writeMiseConfig copies the user's global mise config to dst verbatim, then
// has mise pin every tool whose requested version isn't exact to the version
// in use. mise's own editor keeps comments, options and other sections, and
// understands aliases (nodejs = "22" becomes node = "22.21.1"). It returns
// the tools left as written. mise runs with dst as its global config and a
// throwaway state and cache dir, so nothing outside the repo changes.
func writeMiseConfig(ctx context.Context, r runner.Runner, tools map[string][]miseTool, home, dst string) ([]string, error) {
	data, err := os.ReadFile(globalConfigPath(tools, home))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := writeFile(dst, data, 0o644); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "zakwas-init-mise-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	var parsed struct {
		Tools map[string]any `toml:"tools"`
	}
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("global mise config is not valid TOML: %w", err)
	}
	env := []string{
		"MISE_AUTO_INSTALL=0",
		"MISE_GLOBAL_CONFIG_FILE=" + dst,
		"MISE_STATE_DIR=" + filepath.Join(scratch, "state"),
		"MISE_CACHE_DIR=" + filepath.Join(scratch, "cache"),
		"MISE_YES=1",
	}
	var unpinned []string
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		args, why := pinArgs(name, tools[name], parsed.Tools[name])
		switch {
		case args == nil && why == "":
			continue
		case args == nil:
			unpinned = append(unpinned, name+" ("+why+")")
			continue
		}
		use := runner.Cmd{Name: "mise", Args: append([]string{"use", "--global", "--pin", "--quiet"}, args...), Dir: mise.Dir, Env: env}
		if err := runner.Check(ctx, r, use); err != nil {
			unpinned = append(unpinned, name+" ("+firstLine(err.Error())+")")
		}
	}
	pinned, err := os.ReadFile(dst)
	if err != nil {
		return nil, err
	}
	var check map[string]any
	if err := toml.Unmarshal(pinned, &check); err != nil {
		return nil, fmt.Errorf("pinned mise config %s is not valid TOML: %w", dst, err)
	}
	return unpinned, nil
}

// pinArgs returns the `mise use` arguments pinning name, nil when every
// version is already exact, or why it can't be pinned. Only installed
// versions are pinned: `mise use` would install the others. mise writes
// versions in argument order and the first one is the default, while `mise
// ls` sorts them, so several versions follow the order in entry.
func pinArgs(name string, versions []miseTool, entry any) ([]string, string) {
	if len(versions) > 1 {
		ordered, ok := inConfigOrder(versions, entry)
		if !ok {
			return nil, "several versions in an order init can't read"
		}
		versions = ordered
	}
	exact := true
	var args []string
	for _, t := range versions {
		if t.Version == "" {
			return nil, "no resolved version"
		}
		if !t.Installed {
			return nil, "not installed"
		}
		exact = exact && t.RequestedVersion == t.Version
		args = append(args, name+"@"+t.Version)
	}
	if exact {
		return nil, ""
	}
	return args, ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func inConfigOrder(versions []miseTool, entry any) ([]miseTool, bool) {
	list, ok := entry.([]any)
	if !ok || len(list) != len(versions) {
		return nil, false
	}
	var ordered []miseTool
	for _, item := range list {
		requested, ok := item.(string)
		if table, isTable := item.(map[string]any); isTable {
			requested, ok = table["version"].(string)
		}
		i := slices.IndexFunc(versions, func(t miseTool) bool { return t.RequestedVersion == requested })
		if !ok || i < 0 {
			return nil, false
		}
		ordered = append(ordered, versions[i])
	}
	return ordered, true
}
