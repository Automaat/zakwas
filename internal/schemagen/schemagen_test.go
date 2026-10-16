package schemagen_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
		"agents: {instructions: }\n",
		"agents: {instructions: AGENTS.md}\n",
		"agents: {instructions: [base.md, claude.md], sharedInstructions: true}\n",
		"agents: {permissions: }\n",
		"agents: {permissions: {allow: , review: , deny: }}\n",
		"agents: {permissions: {allow: [git status, {command: sed -n, providers: , justification: }], deny: [{command: rm -rf, justification: use trash}]}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: true}}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: false}}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: }}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: 0oa1abcdEXAMPLE}}}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: '12345', callbackPort: 1, scope: 'read write'}}}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: 65535, scope: 'a:b c/d'}}}}\n",
		"agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: , scope: }}}}\n",
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
		{"instructions as a mapping", "agents: {instructions: {file: a.md}}\n", "instructions"},
		{"empty instructions entry", "agents: {instructions: ['']}\n", "instructions"},
		{"shared instructions text", "agents: {sharedInstructions: yes please}\n", "sharedInstructions"},
		{"empty agent providers", "agents: {providers: []}\n", "providers"},
		{"empty plugin providers", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, providers: []}]}\n", "providers"},
		{"marketplaces as a list", "agents: {marketplaces: [o/sai]}\n", "marketplaces"},
		{"empty marketplace source", "agents: {marketplaces: {sai: ''}}\n", "marketplaces"},
		{"unknown plugin field", "agents: {marketplaces: {sai: o/sai}, plugins: [{id: a@sai, scope: user}]}\n", "scope"},
		{"string mode", "templates:\n  files:\n    - {src: a, dst: ~/a, mode: '0644'}\n", "mode"},
		{"permissions as a list", "agents: {permissions: [git status]}\n", "permissions"},
		{"unknown permission list", "agents: {permissions: {ask: [ls]}}\n", "ask"},
		{"empty permission command", "agents: {permissions: {allow: ['']}}\n", "allow"},
		{"unknown permission field", "agents: {permissions: {deny: [{command: rm, decision: deny}]}}\n", "decision"},
		{"oauth text", "agents: {mcp: {a: {url: 'https://e.com', oauth: yes please}}}\n", "oauth"},
		{"oauth number", "agents: {mcp: {a: {url: 'https://e.com', oauth: 1}}}\n", "oauth"},
		{"oauth list", "agents: {mcp: {a: {url: 'https://e.com', oauth: [c]}}}\n", "oauth"},
		{"oauth without a client id", "agents: {mcp: {a: {url: 'https://e.com', oauth: {callbackPort: 54661}}}}\n", "clientId"},
		{"oauth empty mapping", "agents: {mcp: {a: {url: 'https://e.com', oauth: {}}}}\n", "clientId"},
		{"oauth empty client id", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: ''}}}}\n", "clientId"},
		{"oauth client id with a space", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: 'a b'}}}}\n", "clientId"},
		{"oauth port zero", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: 0}}}}\n", "callbackPort"},
		{"oauth port too high", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: 65536}}}}\n", "callbackPort"},
		{"oauth port text", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: '54661'}}}}\n", "callbackPort"},
		{"oauth port fraction", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, callbackPort: 54661.5}}}}\n", "callbackPort"},
		{"oauth empty scope", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, scope: ''}}}}\n", "scope"},
		{"oauth scope with double spaces", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, scope: 'a  b'}}}}\n", "scope"},
		{"oauth scope list", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, scope: [a]}}}}\n", "scope"},
		{"oauth client secret", "agents: {mcp: {a: {url: 'https://e.com', oauth: {clientId: c, clientSecret: s}}}}\n", "clientSecret"},
		{"permission provider", "agents: {permissions: {allow: [{command: ls, providers: [cursor]}]}}\n", "providers"},
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

func TestMCPOAuthSchemaHasBothForms(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(schema.JSON, &doc); err != nil {
		t.Fatal(err)
	}
	at := func(s any, keys ...string) map[string]any {
		t.Helper()
		m, _ := s.(map[string]any)
		for _, k := range keys {
			var ok bool
			if m, ok = m[k].(map[string]any); !ok {
				t.Fatalf("schema has no %s", k)
			}
		}
		return m
	}
	oauth := at(doc, "properties", "agents", "properties", "mcp", "additionalProperties", "properties", "oauth")
	if got, _ := oauth["type"].([]any); !slices.Equal(got, []any{"boolean", "object", "null"}) {
		t.Errorf("oauth type = %v, want boolean, object and null", got)
	}
	if got, _ := oauth["required"].([]any); !slices.Equal(got, []any{"clientId"}) {
		t.Errorf("oauth required = %v, want clientId", got)
	}
	if oauth["additionalProperties"] != false || oauth["default"] != false {
		t.Errorf("oauth additionalProperties %v default %v", oauth["additionalProperties"], oauth["default"])
	}
	props := at(oauth, "properties")
	for _, name := range []string{"clientId", "callbackPort", "scope"} {
		if d, _ := at(props, name)["description"].(string); d == "" {
			t.Errorf("oauth.%s has no description", name)
		}
	}
	if _, secret := props["clientSecret"]; secret || len(props) != 3 {
		t.Errorf("oauth properties = %v, want clientId, callbackPort and scope only", props)
	}
}

func TestMCPOAuthSchemaAndLoaderAgree(t *testing.T) {
	s := compile(t)
	doc := func(oauth string) string {
		return "agents: {mcp: {a: {url: 'https://e.com/mcp', oauth: " + oauth + "}}}\n"
	}
	blank := []string{`"\u3164"`, `"\u115f"`, `"\u1160"`, `"\uffa0"`, `"\u2800"`}
	invisible := []string{`"\u200b"`, `"\u200d"`, `"\u202e"`, `"\u00ad"`, `"\u034f"`, `"\ufe0f"`, `"\U000e0100"`, `"\u17b4"`, `"\u00a0"`, `"\u2062"`, `"\u180e"`, `"\ue000"`, `"\u0007"`, `"\t"`, `"a b"`, `" a"`, `""`}
	accepted := []string{
		"true", "false", "null", "~", "{clientId: 0oa1abcdEXAMPLE}", "{clientId: '12345'}", "{clientId: 'true'}",
		`{clientId: "a'b"}`, `{clientId: 'a"b'}`, `{clientId: 'a\b'}`, `{clientId: "żółć"}`, `{clientId: "中文-id"}`, `{clientId: "a$b"}`, "{clientId: yes}",
		"{clientId: c, callbackPort: 1}", "{clientId: c, callbackPort: 65535}", "{clientId: c, callbackPort: 0x1F90}", "{clientId: c, callbackPort: 8080.0}", "{clientId: c, callbackPort: 1e3}",
		"{clientId: c, callbackPort: }", "{clientId: c, scope: }", "{clientId: c, scope: 'read write'}", "{clientId: c, scope: 'api:read api:write'}", `{clientId: c, scope: "a'b"}`, `{clientId: c, scope: 'https://x/y?z=1'}`,
	}
	rejected := []string{
		"maybe", "1", "[c]", "{}", "{callbackPort: 1}", "{clientId: }", "{clientId: 12345}", "{clientId: 1.5}", "{clientId: true}", "{clientId: [a]}", "{clientId: {k: v}}",
		"{clientId: c, clientSecret: s}", "{clientId: c, callbackPort: 0}", "{clientId: c, callbackPort: 65536}", "{clientId: c, callbackPort: -1}", "{clientId: c, callbackPort: 8080.5}",
		"{clientId: c, callbackPort: '8080'}", "{clientId: c, callbackPort: [1]}", "{clientId: c, scope: ''}", "{clientId: c, scope: 5}", "{clientId: c, scope: true}", "{clientId: c, scope: [a]}",
		"{clientId: c, scope: 'a  b'}", "{clientId: c, scope: ' a'}", "{clientId: c, scope: 'a '}", `{clientId: c, scope: 'a"b'}`, `{clientId: c, scope: 'a\b'}`, `{clientId: c, scope: "é"}`, `{clientId: c, scope: "a\tb"}`,
	}
	for _, v := range append(blank, invisible...) {
		rejected = append(rejected, "{clientId: "+v+"}")
	}
	for _, v := range accepted {
		check(t, s, doc(v), true)
	}
	for _, v := range rejected {
		check(t, s, doc(v), false)
	}
}

func check(t *testing.T, s *jsonschema.Schema, yamlDoc string, want bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), config.FileName)
	if err := os.WriteFile(path, []byte(yamlDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, loadErr := config.Load(path, t.TempDir())
	schemaErr := validate(t, s, []byte(yamlDoc))
	if (loadErr == nil) != want || (schemaErr == nil) != want {
		t.Errorf("%s: loader accepts %v (%v), schema accepts %v (%v), want %v", strings.TrimSpace(yamlDoc), loadErr == nil, loadErr, schemaErr == nil, schemaErr, want)
	}
}
