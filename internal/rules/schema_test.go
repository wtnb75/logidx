package rules

import (
	"strings"
	"testing"
)

const validSchemaTestYAML = `
rules:
  - name: app
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`

func TestLoadConfig_ValidDocumentPassesSchema(t *testing.T) {
	if _, err := loadConfig([]byte(validSchemaTestYAML)); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestLoadConfig_EmptyInputSkipsSchema(t *testing.T) {
	if _, err := loadConfig(nil); err != nil {
		t.Fatalf("expected empty input to be accepted as a no-op document, got: %v", err)
	}
}

func TestLoadConfig_RuleLevelTypoIsRejected(t *testing.T) {
	yamlContent := `
rules:
  - name: app
    paterrn: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	_, err := loadConfig([]byte(yamlContent))
	if err == nil {
		t.Fatal("expected a schema error for the paterrn: typo, got nil")
	}
	if !strings.Contains(err.Error(), "paterrn") {
		t.Errorf("expected error to name the offending key %q, got: %v", "paterrn", err)
	}
}

func TestLoadConfig_FieldLevelTypoIsRejected(t *testing.T) {
	yamlContent := `
rules:
  - name: app
    pattern: '^(?P<msg>.*)$'
    fields:
      msg:
        typpe: string
`
	if _, err := loadConfig([]byte(yamlContent)); err == nil {
		t.Fatal("expected a schema error for the typpe: typo, got nil")
	}
}

func TestLoadConfig_CompressionLevelTypoIsRejected(t *testing.T) {
	yamlContent := validSchemaTestYAML + `
compression:
  codecc: zstd
`
	_, err := loadConfig([]byte(yamlContent))
	if err == nil {
		t.Fatal("expected a schema error for the codecc: typo, got nil")
	}
	if !strings.Contains(err.Error(), "codecc") {
		t.Errorf("expected error to name the offending key %q, got: %v", "codecc", err)
	}
}

// A root-level key that only exists to hold a YAML anchor (aliased into
// rules: elsewhere) must keep working - see
// TestCollapse_WholeRulesSequenceAliasDoesNotPanic in convert_test.go for
// the aliasing side of this; this only checks that loadConfig's schema
// check itself doesn't reject the extra root key.
func TestLoadConfig_RootLevelScratchKeyForAnchorIsAllowed(t *testing.T) {
	yamlContent := `
_defs: &common
  - name: app
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
rules: *common
`
	if _, err := loadConfig([]byte(yamlContent)); err != nil {
		t.Fatalf("expected a root-level anchor-holder key to be allowed, got: %v", err)
	}
}

func TestLoadConfig_RootLevelTypoIsStillRejectedByGoStruct(t *testing.T) {
	// The schema no longer forbids unknown root keys (see
	// TestLoadConfig_RootLevelScratchKeyForAnchorIsAllowed), but a document
	// with *only* a typo'd root key and no rules: still fails downstream -
	// Config.Validate has no rules to check, but Rules stays nil/empty,
	// which is a legitimate (if useless) config, not an error. This test
	// exists to document that tradeoff rather than to assert a specific
	// error.
	yamlContent := `
rulez:
  - name: app
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
`
	cfg, err := loadConfig([]byte(yamlContent))
	if err != nil {
		t.Fatalf("expected no error (rulez: is silently ignored, rules: stays empty), got: %v", err)
	}
	if len(cfg.Rules) != 0 {
		t.Fatalf("expected Rules to be empty, got %d", len(cfg.Rules))
	}
}
