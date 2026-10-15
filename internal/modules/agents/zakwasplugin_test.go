package agents

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

// repoRoot is the zakwas repository, which publishes the zakwas plugin.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)

func TestZakwasPluginManifests(t *testing.T) {
	root := repoRoot(t)
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name    string `json:"name"`
			Source  string `json:"source"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"))), &market); err != nil {
		t.Fatal(err)
	}
	if market.Name != "zakwas" || len(market.Plugins) != 1 || market.Plugins[0].Name != "zakwas" {
		t.Fatalf("marketplace = %+v, want marketplace zakwas with the one plugin zakwas", market)
	}
	entry := market.Plugins[0]
	dir, ok := pluginDir(root, "", entry.Source)
	if !ok {
		t.Fatalf("plugin source %q is not inside the repo", entry.Source)
	}

	var plugin struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"))), &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Name != entry.Name {
		t.Errorf("plugin.json name %q, marketplace entry %q", plugin.Name, entry.Name)
	}
	if !semver.MatchString(plugin.Version) {
		t.Errorf("plugin version %q is not X.Y.Z", plugin.Version)
	}
	if entry.Version != "" {
		t.Errorf("marketplace entry sets version %q; the release check reads plugin.json only", entry.Version)
	}
}

func TestZakwasSkillTeachesTheProposalFlow(t *testing.T) {
	skill := readFile(t, filepath.Join(repoRoot(t), "plugins", "zakwas", "skills", "zakwas", "SKILL.md"))
	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !ok || !strings.HasPrefix(skill, "---\n") {
		t.Fatal("SKILL.md has no frontmatter")
	}
	for _, key := range []string{"name: zakwas\n", "description: "} {
		if !strings.Contains(front+"\n", key) {
			t.Errorf("frontmatter lacks %q", key)
		}
	}
	for _, want := range []string{
		"zakwas_propose",
		"zakwas propose",
		"zakwas approve ID",
		"Never",
		"zakwas apply",
		"zakwas.yaml",
		"~/.claude/settings.json",
		"high",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("SKILL.md never mentions %q", want)
		}
	}
	for _, personal := range []string{"/Users/", "/home/"} {
		if strings.Contains(skill, personal) {
			t.Errorf("SKILL.md holds a personal path (%s)", personal)
		}
	}
}

func TestZakwasPluginLinksIntoOpencode(t *testing.T) {
	root := repoRoot(t)
	m, _ := newOpencode(t, config.Agents{
		Marketplaces: map[string]config.Marketplace{"zakwas": {Source: root}},
		Plugins:      []config.Plugin{{ID: "zakwas@zakwas"}},
	})
	changes := plan(t, m)
	if got, want := targets(changes), []string{"+ opencode skill zakwas (zakwas@zakwas)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	apply(t, changes)
	assertLink(t, filepath.Join(skillsDir(m), "zakwas"), filepath.Join(root, "plugins", "zakwas", "skills", "zakwas"))
	assertConverged(t, m)
}
