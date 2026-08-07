package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/rules"
)

func TestBuild_ColumnsPreserveDeclarationOrder(t *testing.T) {
	fields := []rules.Field{
		{Name: "status", Type: "int"},
		{Name: "path", Type: "string"},
		{Name: "time", Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
	}

	built, err := Build("nginx_access", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"status", "path", "time"}
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

	var schemaOrder []string
	for _, f := range built.Schema.Fields() {
		schemaOrder = append(schemaOrder, f.Name())
	}
	if len(schemaOrder) != len(want) {
		t.Fatalf("Schema.Fields() has %d fields, want %d", len(schemaOrder), len(want))
	}
	for i, name := range want {
		if schemaOrder[i] != name {
			t.Errorf("Schema.Fields()[%d] = %q, want %q (declaration order, not alphabetical)", i, schemaOrder[i], name)
		}
	}
}

func TestBuild_ColumnOrderSurvivesWritingAndReopeningTheFile(t *testing.T) {
	fields := []rules.Field{
		{Name: "zzz_last", Type: "string"},
		{Name: "aaa_first", Type: "int"},
		{Name: "mmm_mid", Type: "string"},
	}

	built, err := Build("row", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "test.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := parquet.NewGenericWriter[map[string]any](f, built.Schema)
	if _, err := w.Write([]map[string]any{{"zzz_last": "z", "aaa_first": int64(1), "mmm_mid": "m"}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rf.Close() }()
	fi, err := rf.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(rf, fi.Size())
	if err != nil {
		t.Fatalf("open parquet: %v", err)
	}

	want := []string{"zzz_last", "aaa_first", "mmm_mid"}
	var got []string
	for _, field := range pf.Schema().Fields() {
		got = append(got, field.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("on-disk field[%d] = %q, want %q (declaration order should survive round-trip through the file)", i, got[i], name)
		}
	}
}

func TestBuild_FieldTypesMatchDeclaredTypes(t *testing.T) {
	fields := []rules.Field{
		{Name: "status", Type: "int"},
		{Name: "path", Type: "string"},
		{Name: "time", Type: "timestamp", Format: "2006-01-02T15:04:05Z07:00"},
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
	fields := []rules.Field{{Name: "a", Type: "bogus"}}
	_, err := Build("bad", fields)
	if err == nil {
		t.Fatal("expected error for unsupported field type")
	}
}

func TestBuildAll_DeduplicatesByName(t *testing.T) {
	ruleList := []rules.Rule{
		{Name: "dup", Fields: []rules.Field{{Name: "a", Type: "string"}}},
		{Name: "dup", Fields: []rules.Field{{Name: "a", Type: "string"}}},
		{Name: "other", Fields: []rules.Field{{Name: "b", Type: "int"}}},
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

func TestTypeName_RoundTripsWithNodeForType(t *testing.T) {
	for _, name := range []string{"string", "int", "float", "timestamp"} {
		node, err := NodeForType(name)
		if err != nil {
			t.Fatalf("NodeForType(%q): %v", name, err)
		}
		got, err := TypeName(node)
		if err != nil {
			t.Fatalf("TypeName(%q's node): %v", name, err)
		}
		if got != name {
			t.Errorf("TypeName(NodeForType(%q)) = %q, want %q", name, got, name)
		}
	}
}

func TestTypeName_UnsupportedType(t *testing.T) {
	if _, err := TypeName(parquet.Leaf(parquet.BooleanType)); err == nil {
		t.Error("expected error for a parquet type this package doesn't model, got nil")
	}
}
