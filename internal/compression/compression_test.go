package compression

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
	"gopkg.in/yaml.v3"
)

func intPtr(n int) *int { return &n }

func TestResolve_DefaultsToZstdWhenNothingSet(t *testing.T) {
	got := Resolve(Settings{}, Settings{})
	want := Settings{Codec: "zstd"}
	if got != want {
		t.Errorf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_FileOverridesDefault(t *testing.T) {
	got := Resolve(Settings{}, Settings{Codec: "gzip", Level: intPtr(5)})
	want := Settings{Codec: "gzip", Level: intPtr(5)}
	if got.Codec != want.Codec || *got.Level != *want.Level {
		t.Errorf("Resolve() = %+v, want %+v", got, want)
	}
}

func TestResolve_CLIOverridesFile(t *testing.T) {
	got := Resolve(
		Settings{Codec: "lz4", Level: intPtr(3)},
		Settings{Codec: "gzip", Level: intPtr(5)},
	)
	if got.Codec != "lz4" || got.Level == nil || *got.Level != 3 {
		t.Errorf("Resolve() = %+v, want codec=lz4 level=3", got)
	}
}

func TestResolve_CLICodecOnlyKeepsFileLevel(t *testing.T) {
	got := Resolve(
		Settings{Codec: "gzip"},
		Settings{Codec: "gzip", Level: intPtr(7)},
	)
	if got.Codec != "gzip" || got.Level == nil || *got.Level != 7 {
		t.Errorf("Resolve() = %+v, want codec=gzip level=7", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		s       Settings
		wantErr bool
	}{
		{"empty codec valid", Settings{}, false},
		{"zstd default valid", Settings{Codec: "zstd"}, false},
		{"zstd level in range", Settings{Codec: "zstd", Level: intPtr(1)}, false},
		{"zstd level out of range", Settings{Codec: "zstd", Level: intPtr(5)}, true},
		{"gzip level in range", Settings{Codec: "gzip", Level: intPtr(-1)}, false},
		{"gzip level out of range", Settings{Codec: "gzip", Level: intPtr(10)}, true},
		{"brotli level in range", Settings{Codec: "brotli", Level: intPtr(11)}, false},
		{"brotli level out of range", Settings{Codec: "brotli", Level: intPtr(12)}, true},
		{"lz4 level in range", Settings{Codec: "lz4", Level: intPtr(9)}, false},
		{"lz4 level out of range", Settings{Codec: "lz4", Level: intPtr(10)}, true},
		{"snappy with level rejected", Settings{Codec: "snappy", Level: intPtr(1)}, true},
		{"snappy without level ok", Settings{Codec: "snappy"}, false},
		{"uncompressed with level rejected", Settings{Codec: "uncompressed", Level: intPtr(1)}, true},
		{"unknown codec rejected", Settings{Codec: "lzma"}, true},
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

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name      string
		codec     string
		raw       string
		wantLevel *int
		wantErr   bool
	}{
		{"plain integer", "gzip", "5", intPtr(5), false},
		{"negative integer", "gzip", "-1", intPtr(-1), false},
		{"not a number or alias", "gzip", "fastish", nil, true},
		{"zstd fast", "zstd", "fast", intPtr(1), false},
		{"zstd best", "zstd", "best", intPtr(4), false},
		{"zstd normal", "zstd", "normal", nil, false},
		{"empty codec fast defaults to zstd range", "", "fast", intPtr(1), false},
		{"gzip fast", "gzip", "fast", intPtr(-2), false},
		{"gzip best", "gzip", "best", intPtr(9), false},
		{"brotli fast", "brotli", "fast", intPtr(0), false},
		{"brotli best", "brotli", "best", intPtr(11), false},
		{"lz4 fast", "lz4", "fast", intPtr(0), false},
		{"lz4 best", "lz4", "best", intPtr(9), false},
		{"snappy rejects alias", "snappy", "fast", nil, true},
		{"uncompressed rejects alias", "uncompressed", "best", nil, true},
		{"unknown codec rejects alias", "lzma", "fast", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLevel(tt.codec, tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseLevel(%q, %q) error = %v, wantErr %v", tt.codec, tt.raw, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if (got == nil) != (tt.wantLevel == nil) {
				t.Fatalf("ParseLevel(%q, %q) = %v, want %v", tt.codec, tt.raw, got, tt.wantLevel)
			}
			if got != nil && *got != *tt.wantLevel {
				t.Errorf("ParseLevel(%q, %q) = %d, want %d", tt.codec, tt.raw, *got, *tt.wantLevel)
			}
		})
	}
}

func TestSettings_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		wantCodec string
		wantLevel *int
		wantErr   bool
	}{
		{"integer level", "codec: gzip\nlevel: 9\n", "gzip", intPtr(9), false},
		{"no level", "codec: gzip\n", "gzip", nil, false},
		{"alias fast", "codec: zstd\nlevel: fast\n", "zstd", intPtr(1), false},
		{"alias best", "codec: gzip\nlevel: best\n", "gzip", intPtr(9), false},
		{"alias normal", "codec: gzip\nlevel: normal\n", "gzip", nil, false},
		{"alias with default codec", "level: best\n", "", intPtr(4), false},
		{"unknown alias", "codec: zstd\nlevel: turbo\n", "", nil, true},
		{"alias on snappy rejected", "codec: snappy\nlevel: fast\n", "", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s Settings
			err := yaml.Unmarshal([]byte(tt.doc), &s)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Unmarshal(%q) error = %v, wantErr %v", tt.doc, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if s.Codec != tt.wantCodec {
				t.Errorf("Codec = %q, want %q", s.Codec, tt.wantCodec)
			}
			if (s.Level == nil) != (tt.wantLevel == nil) {
				t.Fatalf("Level = %v, want %v", s.Level, tt.wantLevel)
			}
			if s.Level != nil && *s.Level != *tt.wantLevel {
				t.Errorf("Level = %d, want %d", *s.Level, *tt.wantLevel)
			}
		})
	}
}

// writtenCodec writes one row to a temp Parquet file using s.WriterOption()
// and returns the compression codec recorded in the file's metadata, to
// confirm WriterOption actually wires up the codec it claims to.
func writtenCodec(t *testing.T, s Settings) format.CompressionCodec {
	t.Helper()

	schema := parquet.SchemaOf(struct{ X int64 }{})
	path := filepath.Join(t.TempDir(), "out.parquet")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := parquet.NewGenericWriter[struct{ X int64 }](f, schema, s.WriterOption())
	if _, err := w.Write([]struct{ X int64 }{{X: 1}}); err != nil {
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

	return pf.Metadata().RowGroups[0].Columns[0].MetaData.Codec
}

func TestWriterOption_SelectsExpectedCodec(t *testing.T) {
	tests := []struct {
		codec string
		want  format.CompressionCodec
	}{
		{"", format.Zstd},
		{"zstd", format.Zstd},
		{"uncompressed", format.Uncompressed},
		{"snappy", format.Snappy},
		{"gzip", format.Gzip},
		{"brotli", format.Brotli},
		{"lz4", format.Lz4Raw},
	}

	for _, tt := range tests {
		t.Run(tt.codec, func(t *testing.T) {
			got := writtenCodec(t, Settings{Codec: tt.codec})
			if got != tt.want {
				t.Errorf("codec in file = %v, want %v", got, tt.want)
			}
		})
	}
}
