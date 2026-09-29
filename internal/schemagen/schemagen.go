// Package schemagen builds the zakwas.yaml JSON Schema from the config types,
// so the schema can't drift from what the loader accepts.
package schemagen

import (
	"encoding/json"
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
	}
	s := r.Reflect(&config.Config{})
	s.ID = schema.ID
	s.Title = "zakwas.yaml"
	s.Description = "Declarative macOS setup converged by zakwas: files, links, templates, brew, mise, defaults, system, commands."
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(htmlUnescaper.Replace(string(out))), '\n'), nil
}

// htmlUnescaper undoes encoding/json's HTML escaping (Schema's MarshalJSON
// ignores SetEscapeHTML), keeping descriptions readable in the raw file.
var htmlUnescaper = strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&")
