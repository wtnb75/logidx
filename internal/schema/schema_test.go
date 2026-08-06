package schema

import (
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

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

func TestBuild_FieldTypesMatchDeclaredTypes(t *testing.T) {
	fields := map[string]rules.Field{
		"status": {Type: "int"},
		"path":   {Type: "string"},
		"time":   {Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
	}

	built, err := Build("nginx_access", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byName := map[string]parquet.Field{}
	for _, f := range built.Schema.Fields() {
		byName[f.Name()] = f
	}

	cases := []struct {
		name       string
		wantKind   parquet.Kind
		wantPrefix string
	}{
		{"path", parquet.ByteArray, "STRING"},
		{"status", parquet.Int64, "INT"},
		{"time", parquet.Int64, "TIMESTAMP"},
	}

	for _, c := range cases {
		f, ok := byName[c.name]
		if !ok {
			t.Fatalf("schema has no field %q", c.name)
		}
		if !f.Required() {
			t.Errorf("field %q: expected Required, got optional/repeated", c.name)
		}
		gotKind := f.Type().Kind()
		if gotKind != c.wantKind {
			t.Errorf("field %q: Kind() = %s, want %s", c.name, gotKind, c.wantKind)
		}
		gotStr := f.Type().String()
		if !strings.HasPrefix(gotStr, c.wantPrefix) {
			t.Errorf("field %q: Type().String() = %q, want prefix %q", c.name, gotStr, c.wantPrefix)
		}
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
