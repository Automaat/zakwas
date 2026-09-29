// Package schema holds the JSON Schema for zakwas.yaml, generated from the
// config types by internal/schemagen.
package schema

import _ "embed"

//go:generate go run ../internal/schemagen/gen zakwas.schema.json

// ID is where editors fetch the published schema.
const ID = "https://raw.githubusercontent.com/Automaat/zakwas/main/schema/zakwas.schema.json"

// JSON is the committed schema document.
//
//go:embed zakwas.schema.json
var JSON []byte
