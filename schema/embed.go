// Package jsonschema embeds the JSON Schema (schema/rules.schema.json) that
// describes rules.yaml, for editor integration (yaml-language-server) and
// for `logidx schema` to print. Named jsonschema, not schema, to avoid
// confusion with internal/schema (which builds Parquet schemas, an
// unrelated concept).
package jsonschema

import _ "embed"

//go:embed rules.schema.json
var RulesSchema string
