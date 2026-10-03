// Package schemagen builds the zakwas.yaml JSON Schema from the config types,
// so the schema can't drift from what the loader accepts.
package schemagen

import (
	"encoding/json"
	"errors"
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
	if err := scalarShorthands(root); err != nil {
		return nil, err
	}
	root.ID = schema.ID
	root.Title = "zakwas.yaml"
	root.Description = "Declarative macOS setup converged by zakwas: files, links, templates, brew, mise, agents, defaults, system, commands."
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
	if s.AdditionalProperties != nil {
		allowNull(s.AdditionalProperties)
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

// scalarShorthands lets an agents marketplace be written as its source
// string and a plugin as its id, matching their UnmarshalYAML. Widening
// "type" keeps the object's properties for editors, where anyOf would hide
// them.
func scalarShorthands(root *jsonschema.Schema) error {
	agents := property(root, "agents")
	marketplaces := property(agents, "marketplaces")
	plugins := property(agents, "plugins")
	if marketplaces == nil || plugins == nil || marketplaces.AdditionalProperties == nil || plugins.Items == nil {
		return errors.New("schemagen: agents.marketplaces or agents.plugins not found")
	}
	for _, s := range []*jsonschema.Schema{marketplaces.AdditionalProperties, plugins.Items} {
		one := uint64(1)
		s.MinLength = &one
		if s.Extras == nil {
			s.Extras = map[string]any{}
		}
		s.Extras["type"] = []string{"string", s.Type}
		s.Type = ""
	}
	return nil
}

func property(s *jsonschema.Schema, name string) *jsonschema.Schema {
	if s == nil || s.Properties == nil {
		return nil
	}
	p, _ := s.Properties.Get(name)
	return p
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
