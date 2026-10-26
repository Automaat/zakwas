package layers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Automaat/zakwas/internal/config"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	r, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := r.For("/any"); len(got) != 0 {
		t.Errorf("layers = %v", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	r, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	work := []config.Layer{{Name: "work", Path: "/w/zakwas.yaml"}, {Name: "lab", Path: "/l/zakwas.yaml"}}
	r.Set("/main", work)
	r.Set("/other", []config.Layer{{Name: "x", Path: "/x/zakwas.yaml"}})
	if err := r.Save(home); err != nil {
		t.Fatal(err)
	}

	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if layers := got.For("/main"); len(layers) != 2 || layers[0] != work[0] || layers[1] != work[1] {
		t.Errorf("main layers = %v, want %v in registration order", layers, work)
	}
	if layers := got.For("/other"); len(layers) != 1 {
		t.Errorf("other layers = %v", layers)
	}
	if layers := got.For("/unrelated"); len(layers) != 0 {
		t.Errorf("a root with no registrations got %v", layers)
	}
	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 1`) {
		t.Errorf("registry is not versioned:\n%s", data)
	}
}

func TestSetNoneDropsTheRoot(t *testing.T) {
	r := &Registry{Roots: map[string][]config.Layer{"/main": {{Name: "a", Path: "/a"}}}}
	r.Set("/main", nil)
	if _, ok := r.Roots["/main"]; ok {
		t.Error("an empty root stayed in the registry")
	}
}

func TestForReturnsACopy(t *testing.T) {
	r := &Registry{Roots: map[string][]config.Layer{"/main": {{Name: "a", Path: "/a"}}}}
	r.For("/main")[0].Name = "changed"
	if got := r.For("/main")[0].Name; got != "a" {
		t.Errorf("name = %q", got)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":       "{",
		"a newer format": `{"version": 2, "roots": {}}`,
		"no version":     `{"roots": {}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(Path(home)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(home), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(home)
			if err == nil || !strings.Contains(err.Error(), Path(home)) {
				t.Errorf("err = %v, want one naming %s", err, Path(home))
			}
		})
	}
}
