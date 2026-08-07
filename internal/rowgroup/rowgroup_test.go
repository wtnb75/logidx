package rowgroup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
)

func int64Ptr(n int64) *int64 { return &n }

func TestResolve_NoSettingLeavesMaxRowsNil(t *testing.T) {
	got := Resolve(Settings{}, Settings{})
	if got.MaxRows != nil {
		t.Errorf("Resolve() = %+v, want MaxRows nil", got)
	}
}

func TestResolve_FileOverridesDefault(t *testing.T) {
	got := Resolve(Settings{}, Settings{MaxRows: int64Ptr(1000)})
	if got.MaxRows == nil || *got.MaxRows != 1000 {
		t.Errorf("Resolve() = %+v, want MaxRows=1000", got)
	}
}

func TestResolve_CLIOverridesFile(t *testing.T) {
	got := Resolve(Settings{MaxRows: int64Ptr(500)}, Settings{MaxRows: int64Ptr(1000)})
	if got.MaxRows == nil || *got.MaxRows != 500 {
		t.Errorf("Resolve() = %+v, want MaxRows=500", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		s       Settings
		wantErr bool
	}{
		{"unset valid", Settings{}, false},
		{"positive valid", Settings{MaxRows: int64Ptr(1)}, false},
		{"zero invalid", Settings{MaxRows: int64Ptr(0)}, true},
		{"negative invalid", Settings{MaxRows: int64Ptr(-1)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.s.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOption_UnsetReturnsNotOK(t *testing.T) {
	_, ok := Settings{}.Option()
	if ok {
		t.Error("Option() ok = true, want false when MaxRows is unset")
	}
}

func TestOption_SetSplitsIntoMultipleRowGroups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	schema := parquet.SchemaOf(struct{ X int64 }{})
	opt, ok := Settings{MaxRows: int64Ptr(2)}.Option()
	if !ok {
		t.Fatal("Option() ok = false, want true")
	}
	w := parquet.NewGenericWriter[struct{ X int64 }](f, schema, opt)

	rows := make([]struct{ X int64 }, 5)
	for i := range rows {
		rows[i].X = int64(i)
	}
	if _, err := w.Write(rows); err != nil {
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
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = rf.Close() }()
	fi, err := rf.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	pf, err := parquet.OpenFile(rf, fi.Size())
	if err != nil {
		t.Fatalf("open parquet file: %v", err)
	}

	rowGroups := pf.Metadata().RowGroups
	if len(rowGroups) != 3 {
		t.Fatalf("NumRowGroups = %d, want 3 for 5 rows at MaxRows=2", len(rowGroups))
	}
	wantCounts := []int64{2, 2, 1}
	for i, rg := range rowGroups {
		if rg.NumRows != wantCounts[i] {
			t.Errorf("row group %d NumRows = %d, want %d", i, rg.NumRows, wantCounts[i])
		}
	}
}
