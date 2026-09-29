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
	body := r.Reflect(&config.Config{})
	body.Version = ""
	allowNull(body)
	root := nullable(body)
	root.Version = jsonschema.Version
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
		AdditionalProperties: &jsonschema.Schema{AnyOf: []*jsonschema.Schema{
			{Type: "string"}, {Type: "number"}, {Type: "boolean"}, {Type: "null"},
		}},
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
			p.Value = nullable(p.Value)
		}
	}
}

// nullable wraps s in anyOf [s, null], keeping the annotations on the wrapper
// where editors show them.
func nullable(s *jsonschema.Schema) *jsonschema.Schema {
	w := &jsonschema.Schema{
		Description: s.Description,
		Default:     s.Default,
		AnyOf:       []*jsonschema.Schema{s, {Type: "null"}},
	}
	s.Description, s.Default = "", nil
	return w
}

// htmlUnescaper undoes encoding/json's HTML escaping (Schema's MarshalJSON
// ignores SetEscapeHTML), keeping descriptions readable in the raw file.
var htmlUnescaper = strings.NewReplacer(`<`, "<", `>`, ">", `&`, "&")
