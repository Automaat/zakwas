// Package schemagen builds the zakwas.yaml JSON Schema from the config types,
// so the schema can't drift from what the loader accepts.
package schemagen

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/schema"
)

// Generate returns the schema document as indented JSON.
func Generate() ([]byte, error) {
	r := jsonschema.Reflector{
		FieldNameTag:               "yaml",
		RequiredFromJSONSchemaTags: true,
		DoNotReference:             true,
		Anonymous:                  true,
		Mapper:                     scalarMap,
	}
	root := r.Reflect(&config.Config{})
	allowNull(root)
	orNull(root)
	root.ID = schema.ID
	root.Title = "zakwas.yaml"
	root.Description = "Declarative macOS setup converged by zakwas: files, links, templates, brew, mise, defaults, system, commands."
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(htmlUnescaper.Replace(string(out))), '\n'), nil
}

// scalarMap matches how YAML decodes into map[string]string: any scalar
// (8080, true, empty) becomes its text.
func scalarMap(t reflect.Type) *jsonschema.Schema {
	if t != reflect.TypeFor[map[string]string]() {
		return nil
	}
	return &jsonschema.Schema{
		Type: "object",
		AdditionalProperties: &jsonschema.Schema{
			Extras: map[string]any{"type": []string{"string", "number", "boolean", "null"}},
		},
	}
}

// allowNull accepts null for every optional property: YAML reads `brew:` or
// `prune:` with nothing after it as null, which the loader treats as unset.
// Required properties stay strict, since the loader rejects them empty.
func allowNull(s *jsonschema.Schema) {
	if s.Items != nil {
		allowNull(s.Items)
	}
	if s.Properties == nil {
		return
	}
	for p := s.Properties.Oldest(); p != nil; p = p.Next() {
		allowNull(p.Value)
		if !slices.Contains(s.Required, p.Key) {
			orNull(p.Value)
		}
	}
}

// orNull turns "type": T into "type": [T, "null"]. An anyOf wrapper would
// validate the same, but editors then stop offering key skeletons and enum
// descriptions.
func orNull(s *jsonschema.Schema) {
	if s.Type == "" {
		return
	}
	if s.Extras == nil {
		s.Extras = map[string]any{}
	}
	s.Extras["type"] = []string{s.Type, "null"}
	s.Type = ""
	if s.Enum != nil {
		s.Enum = append(s.Enum, nil)
	}
}

// htmlUnescaper undoes encoding/json's HTML escaping (Schema's MarshalJSON
// ignores SetEscapeHTML), keeping descriptions readable in the raw file.
var htmlUnescaper = strings.NewReplacer(jsonEscaped("<"), "<", jsonEscaped(">"), ">", jsonEscaped("&"), "&")

func jsonEscaped(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}
