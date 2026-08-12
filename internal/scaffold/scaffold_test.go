package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)

func TestTemplateLoadsAsValidRulesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(Template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	cfg, err := rules.Load(path)
	if err != nil {
		t.Fatalf("rules.Load(scaffold template): %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Name != "example" {
		t.Errorf("rule name = %q, want %q", cfg.Rules[0].Name, "example")
	}
	if len(cfg.Rules[0].Fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(cfg.Rules[0].Fields))
	}
}

func TestTemplateIsNonEmpty(t *testing.T) {
	if Template == "" {
		t.Fatal("Template is empty")
	}
}
