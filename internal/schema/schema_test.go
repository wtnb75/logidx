package schema

import (
	"testing"

	"logidx/internal/rules"
)

func TestBuild_ColumnsAreSortedByName(t *testing.T) {
	fields := map[string]rules.Field{
		"status": {Type: "int"},
		"path":   {Type: "string"},
		"time":   {Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
	}

	built, err := Build("nginx_access", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"path", "status", "time"}
	if len(built.Columns) != len(want) {
		t.Fatalf("got %d columns, want %d", len(built.Columns), len(want))
	}
	for i, name := range want {
		if built.Columns[i] != name {
			t.Errorf("column[%d] = %q, want %q", i, built.Columns[i], name)
		}
	}
	if built.Schema == nil {
		t.Fatal("expected non-nil Schema")
	}
}

func TestBuild_UnsupportedTypeIsError(t *testing.T) {
	fields := map[string]rules.Field{"a": {Type: "bogus"}}
	_, err := Build("bad", fields)
	if err == nil {
		t.Fatal("expected error for unsupported field type")
	}
}

func TestBuildAll_DeduplicatesByName(t *testing.T) {
	ruleList := []rules.Rule{
		{Name: "dup", Fields: map[string]rules.Field{"a": {Type: "string"}}},
		{Name: "dup", Fields: map[string]rules.Field{"a": {Type: "string"}}},
		{Name: "other", Fields: map[string]rules.Field{"b": {Type: "int"}}},
	}

	all, err := BuildAll(ruleList)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 built schemas, got %d", len(all))
	}
	if _, ok := all["dup"]; !ok {
		t.Error("expected schema for name dup")
	}
	if _, ok := all["other"]; !ok {
		t.Error("expected schema for name other")
	}
}
