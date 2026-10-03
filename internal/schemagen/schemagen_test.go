package schemagen_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/txtar"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/schemagen"
	"github.com/Automaat/zakwas/schema"
)

const repoRoot = "../.."

func TestCommittedSchemaIsCurrent(t *testing.T) {
	want, err := schemagen.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(schema.JSON, want) {
		t.Fatal("schema/zakwas.schema.json is stale: run `mise run schema`")
	}
}

// Editors lose key skeletons and enum descriptions behind anyOf wrappers,
// so nullability must stay a type array.
func TestSchemaIsEditorFriendly(t *testing.T) {
	if bytes.Contains(schema.JSON, []byte(`"anyOf"`)) {
		t.Error("schema uses anyOf; express null as \"type\": [T, \"null\"]")
	}
	for _, c := range []string{"<", ">", "&"} {
		escaped, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(schema.JSON, bytes.Trim(escaped, `"`)) {
			t.Errorf("schema escapes %q", c)
		}
	}
}

func TestEveryPropertyIsDescribed(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(schema.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(path string, s map[string]any)
	walk = func(path string, s map[string]any) {
		if items, ok := s["items"].(map[string]any); ok {
			walk(path+"[]", items)
		}
		anyOf, _ := s["anyOf"].([]any)
		for _, sub := range anyOf {
			walk(path, sub.(map[string]any))
		}
		props, _ := s["properties"].(map[string]any)
		for name, p := range props {
			prop := p.(map[string]any)
			if d, _ := prop["description"].(string); d == "" {
				t.Errorf("%s.%s has no description", path, name)
			}
			walk(path+"."+name, prop)
		}
	}
	walk("", doc)
}

func compile(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.JSON))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schema.ID, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schema.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, data []byte) error {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

// configs returns the example config and every zakwas.yaml embedded in the
// e2e scripts, keyed by where they came from.
func configs(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	example, err := os.ReadFile(filepath.Join(repoRoot, "examples", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	out["examples/"+config.FileName] = example
	scripts, err := filepath.Glob(filepath.Join(repoRoot, "cmd", "zakwas", "testdata", "script", "*.txtar"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range scripts {
		ar, err := txtar.ParseFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range ar.Files {
			if filepath.Base(f.Name) == config.FileName {
				out[filepath.Base(path)+":"+f.Name] = f.Data
			}
		}
	}
	if len(out) < 2 {
		t.Fatalf("found only %d configs", len(out))
	}
	return out
}

// Valid configs must pass both the schema and the loader, so the schema
// never rejects what zakwas accepts.
func TestValidConfigs(t *testing.T) {
	s := compile(t)
	for name, data := range configs(t) {
		t.Run(name, func(t *testing.T) {
			if err := validate(t, s, data); err != nil {
				t.Errorf("schema: %v", err)
			}
			path := filepath.Join(t.TempDir(), config.FileName)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path, t.TempDir()); err != nil {
				t.Errorf("loader: %v", err)
			}
		})
	}
}

// Hand-written variants the loader accepts: empty (null) sections and
// fields, scalar template vars, 0o modes.
func TestLoaderAcceptedConfigs(t *testing.T) {
	s := compile(t)
	for _, doc := range []string{
		"---\n",
		"files:\n",
		"links:\n",
		"brew:\n",
		"mise:\n",
		"system:\n",
		"protect:\n",
		"commands:\n",
		"defaults:\n",
		"templates:\n  vars:\n",
		"system:\n  sshKey:\n",
		"system: {dirs: , sudoTouchID: }\n",
		"protect: {immutable: }\n",
		"mise: {config: x, prune: }\n",
		"brew: {file: x, cleanup: }\n",
		"templates: {vars: {port: 8080, debug: true, ratio: 1.5, empty: }}\n",
		"templates: {files: [{src: a, dst: ~/a, mode: }]}\n",
		"defaults: [{domain: d, key: k, value: 1, restart: , currentHost: }]\n",
		"system: {sshKey: {path: ~/k, comment: }}\n",
		"system: {dirs: [{path: ~/.ssh, mode: 0o700}]}\n",
		"templates: {files: [{src: a, dst: ~/a, mode: 0o600}]}\n",
		"agents:\n",
		"agents: {providers: , upgrade: , prune: , marketplaces: , plugins: }\n",
		"agents: {marketplaces: {sai: o/sai, l: {source: ./x, providers: }}, plugins: [a@sai, {id: b@sai, providers: }]}\n",
		"agents: {providers: [claude], marketplaces: {sai: {source: o/sai, providers: [claude]}}, plugins: [{id: a@sai, providers: [claude]}]}\n",
	} {
		t.Run(doc, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), config.FileName)
			if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path, t.TempDir()); err != nil {
				t.Fatalf("loader: %v", err)
			}
			if err := validate(t, s, []byte(doc)); err != nil {
				t.Errorf("schema: %v", err)
			}
		})
	}
}

// Editors parse YAML 1.2, where 0700 is decimal 700 and fails the schema;
// documented modes must use 0o.
func TestDocumentedModesUseOctalPrefix(t *testing.T) {
	legacy := regexp.MustCompile(`mode:\s*0[0-7]`)
	for _, name := range []string{"examples/zakwas.yaml", "docs/config.md", "README.md"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if m := legacy.Find(data); m != nil {
			t.Errorf("%s: %q, write modes as 0o700", name, m)
		}
	}
}

func TestInvalidConfigs(t *testing.T) {
	s := compile(t)
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"unknown top-level key", "packages: []\n", "packages"},
		{"unknown nested key", "brew: {file: Brewfile, clean: zap}\n", "clean"},
		{"wrong cleanup enum", "brew: {file: Brewfile, cleanup: all}\n", "cleanup"},
		{"missing brew file", "brew: {cleanup: zap}\n", "file"},
		{"missing dst", "files:\n  - {src: dotfiles/zshrc}\n", "dst"},
		{"missing command run", "commands:\n  - {name: x, check: 'true'}\n", "run"},
		{"list value in defaults", "defaults:\n  - {domain: d, key: k, value: [1]}\n", "value"},
		{"decimal mode", "system:\n  dirs:\n    - {path: ~/.ssh, mode: 700}\n", "mode"},
		{"null required field", "files:\n  - {src: , dst: ~/a}\n", "src"},
		{"list template var", "templates: {vars: {x: [1]}}\n", "vars"},
		{"unknown agent provider", "agents: {providers: [cursor]}\n", "providers"},
		{"marketplaces as a list", "agents: {marketplaces: [o/sai]}\n", "marketplaces"},
		{"empty marketplace source", "agents: {marketplaces: {sai: ''}}\n", "marketplaces"},
		{"unknown plugin field", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, scope: user}]}\n", "scope"},
		{"string mode", "templates:\n  files:\n    - {src: a, dst: ~/a, mode: '0644'}\n", "mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(t, s, []byte(tc.yaml))
			if err == nil {
				t.Fatal("schema accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			path := filepath.Join(t.TempDir(), config.FileName)
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path, t.TempDir()); err == nil {
				t.Error("loader accepted it")
			}
		})
	}
}
