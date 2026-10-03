package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pluginDir resolves the relative source of an in-repo plugin: "./x" from
// the marketplace root, a bare "x" from metadata.pluginRoot. Paths that leave
// the root, URLs and absolute paths are not in the repo.
func pluginDir(root, pluginRoot, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, ":") {
		return "", false
	}
	dir := filepath.Join(root, rel)
	if !strings.HasPrefix(rel, "./") {
		if pluginRoot == "" {
			return "", false
		}
		dir = filepath.Join(root, pluginRoot, rel)
	}
	if clean := filepath.Clean(root); dir != clean && !strings.HasPrefix(dir, clean+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}

// manifestPaths are where a marketplace keeps its manifest: Claude's, then
// the one Codex reads too.
var manifestPaths = []string{
	filepath.Join(".claude-plugin", "marketplace.json"),
	filepath.Join(".agents", "plugins", "marketplace.json"),
}

type marketplaceManifest struct {
	path     string
	Name     string `json:"name"`
	Metadata struct {
		PluginRoot string `json:"pluginRoot"`
	} `json:"metadata"`
	Plugins []struct {
		Name   string          `json:"name"`
		Source json.RawMessage `json:"source"`
		Skills json.RawMessage `json:"skills"`
	} `json:"plugins"`
}

// errNotInRepo marks a plugin its marketplace fetches from elsewhere.
var errNotInRepo = errors.New("not stored in the marketplace repo")

// manifests are the manifests a marketplace checkout has, in
// manifestPaths order.
type manifests []marketplaceManifest

// readManifests reads every manifest a marketplace checkout has; a repo may
// list Codex-only plugins in .agents only.
func readManifests(root string) (manifests, error) {
	var found manifests
	for _, rel := range manifestPaths {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		m := marketplaceManifest{path: path}
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		found = append(found, m)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s has no %s", root, strings.Join(manifestPaths, " or "))
	}
	return found, nil
}

// otherName returns a manifest name other than want, if any manifest has one.
func (ms manifests) otherName(want string) string {
	for _, m := range ms {
		if m.Name != "" && m.Name != want {
			return m.Name
		}
	}
	return ""
}

// pluginPath finds the directory of an in-repo plugin in the first
// manifest listing it, and the extra skill paths its entry declares. A
// source is a relative path, or Codex's {"source": "local", "path": …};
// plugins fetched from elsewhere are reported as such.
func (ms manifests) pluginPath(root, plugin string) (string, []string, error) {
	paths := make([]string, 0, len(ms))
	for _, m := range ms {
		paths = append(paths, m.path)
		for _, p := range m.Plugins {
			if p.Name == plugin {
				dir, err := m.dir(root, plugin, p.Source)
				return dir, stringList(p.Skills), err
			}
		}
	}
	return "", nil, fmt.Errorf("plugin %q is not in %s", plugin, strings.Join(paths, " or "))
}

func (m marketplaceManifest) dir(root, plugin string, source json.RawMessage) (string, error) {
	var rel string
	if json.Unmarshal(source, &rel) != nil {
		var local struct {
			Source string `json:"source"`
			Path   string `json:"path"`
		}
		if json.Unmarshal(source, &local) != nil || local.Source != "local" {
			return "", fmt.Errorf("plugin %q in %s is %w (source %s)", plugin, m.path, errNotInRepo, source)
		}
		rel = local.Path
	}
	dir, ok := pluginDir(root, m.Metadata.PluginRoot, rel)
	if !ok {
		return "", fmt.Errorf("plugin %q in %s: source %s is not a path inside the marketplace repo", plugin, m.path, source)
	}
	return dir, nil
}

// stringList reads a JSON string or list of strings; anything else is
// empty.
func stringList(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

type skillDir struct {
	name, path string
}

// pluginSkills lists the skills of a plugin, sorted by name: the paths its
// plugin.json or marketplace entry declares under "skills", each a skill
// directory or a directory of them, or else each skills/<name> holding a
// SKILL.md. A declared list replaces the default, since plugins sharing
// one repo (anthropics/skills) each pick their own subset. Paths outside
// root are ignored; the first skill of a name wins.
func pluginSkills(root, dir string, extra []string) ([]skillDir, error) {
	var own struct {
		Skills json.RawMessage `json:"skills"`
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &own); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, ".claude-plugin", "plugin.json"), err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	seen := map[string]bool{}
	var skills []skillDir
	add := func(name, path string) {
		if !seen[name] {
			seen[name] = true
			skills = append(skills, skillDir{name: name, path: path})
		}
	}
	clean := filepath.Clean(root)
	paths := append(stringList(own.Skills), extra...)
	if len(paths) == 0 {
		paths = []string{"skills"}
	}
	for _, rel := range paths {
		if rel == "" || filepath.IsAbs(rel) {
			continue
		}
		base := filepath.Join(dir, rel)
		if base != clean && !strings.HasPrefix(base, clean+string(filepath.Separator)) {
			continue
		}
		if isSkill(base) {
			add(filepath.Base(base), base)
			continue
		}
		entries, err := os.ReadDir(base)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if p := filepath.Join(base, e.Name()); !strings.HasPrefix(e.Name(), ".") && isSkill(p) {
				add(e.Name(), p)
			}
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].name < skills[j].name })
	return skills, nil
}

func isSkill(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}
