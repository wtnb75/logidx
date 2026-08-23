package rules

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	rulesschema "github.com/wtnb75/logidx/schema"
)

// compiledRulesSchema lazily compiles the embedded JSON Schema
// (schema/rules.schema.json) once per process. It exists to catch
// unknown/typo'd keys (e.g. "paterrn:" instead of "pattern:"), which
// gopkg.in/yaml.v3's plain Unmarshal silently drops - and which even
// (*yaml.Decoder).KnownFields(true) would not catch here, because Rule,
// Field, and compression.Settings each implement UnmarshalYAML by calling
// yaml.Node.Decode, which always builds its own fresh, non-strict decoder
// regardless of the outer Decoder's KnownFields setting.
var compiledRulesSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("rules.schema.json", strings.NewReader(rulesschema.RulesSchema)); err != nil {
		return nil, fmt.Errorf("add embedded rules.schema.json: %w", err)
	}
	return compiler.Compile("rules.schema.json")
})

// validateAgainstSchema checks data's structure against the embedded JSON
// Schema before it is decoded into Config - see compiledRulesSchema for why
// this needs to be a separate pass rather than stricter YAML decoding.
func validateAgainstSchema(data []byte) error {
	sch, err := compiledRulesSchema()
	if err != nil {
		return fmt.Errorf("compile rules.yaml schema: %w", err)
	}

	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse rules YAML: %w", err)
	}
	if raw == nil {
		// Empty input decodes to nil - Load/Expand/Collapse all treat this
		// as a valid, do-nothing document (see Expand's own "empty input
		// decodes to a zero Node" handling in convert.go), not an object
		// missing its properties, so there's nothing to check here.
		return nil
	}

	// jsonschema validates JSON-canonical types (map[string]any, []any,
	// float64/string/bool/nil); round-trip through encoding/json to
	// normalize what yaml.v3's generic decode produced.
	jsonData, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("normalize rules YAML for schema check: %w", err)
	}
	var doc any
	if err := json.Unmarshal(jsonData, &doc); err != nil {
		return fmt.Errorf("normalize rules YAML for schema check: %w", err)
	}

	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("rules.yaml schema: %w", err)
	}
	return nil
}
