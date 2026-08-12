package jsonschema

import (
	"encoding/json"
	"testing"
)

func TestRulesSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(RulesSchema), &v); err != nil {
		t.Fatalf("RulesSchema is not valid JSON: %v", err)
	}
}

func TestRulesSchemaIsNonEmpty(t *testing.T) {
	if RulesSchema == "" {
		t.Fatal("RulesSchema is empty")
	}
}
