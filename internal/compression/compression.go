// Package compression selects the Parquet page compression codec and level
// used when writing output files, and resolves that choice from CLI flags,
// the rules config file, and a built-in default, in that priority order.
package compression

import (
	"fmt"
	"strconv"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"
	"github.com/parquet-go/parquet-go/compress/brotli"
	"github.com/parquet-go/parquet-go/compress/gzip"
	"github.com/parquet-go/parquet-go/compress/lz4"
	"github.com/parquet-go/parquet-go/compress/snappy"
	"github.com/parquet-go/parquet-go/compress/uncompressed"
	"github.com/parquet-go/parquet-go/compress/zstd"
	"gopkg.in/yaml.v3"
)

// DefaultCodec is used when neither a CLI flag nor the config file selects
// a codec.
const DefaultCodec = "zstd"

// lz4Levels maps the 0-9 level scale exposed to users onto the codec's own
// named levels (Fast, Level1..Level9).
var lz4Levels = [...]lz4.Level{
	lz4.Fast,
	lz4.Level1, lz4.Level2, lz4.Level3, lz4.Level4, lz4.Level5,
	lz4.Level6, lz4.Level7, lz4.Level8, lz4.Level9,
}

// Settings selects a Parquet page compression codec and an optional
// codec-specific level. The zero value means "unset" (Codec == "" and
// Level == nil), used as an input to Resolve; Resolve always returns a
// Settings with a non-empty Codec.
type Settings struct {
	Codec string `yaml:"codec" json:"codec"`
	Level *int   `yaml:"level" json:"level,omitempty"`
}

// UnmarshalYAML lets `level:` be either a plain integer or one of the named
// aliases "fast", "normal", "best" (see levelAlias), resolved against the
// sibling `codec:` field in the same mapping.
func (s *Settings) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Codec string    `yaml:"codec"`
		Level yaml.Node `yaml:"level"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.Codec = raw.Codec

	switch {
	case raw.Level.Kind == 0:
		s.Level = nil
	case raw.Level.Tag == "!!str":
		level, err := levelAlias(raw.Codec, raw.Level.Value)
		if err != nil {
			return err
		}
		s.Level = level
	default:
		var n int
		if err := raw.Level.Decode(&n); err != nil {
			return fmt.Errorf("compression level: want an integer or one of: fast, normal, best")
		}
		s.Level = &n
	}
	return nil
}

// levelAlias resolves a named compression level to a concrete numeric level
// for codec, using the same per-codec ranges as Validate: "fast" is the
// fastest/loosest end of the range, "best" is the highest-compression end,
// and "normal" means "use the codec's own built-in default" - the same as
// leaving Level unset (nil).
func levelAlias(codec, name string) (*int, error) {
	var min, max int
	switch codec {
	case "", "zstd":
		min, max = 1, 4
	case "gzip":
		min, max = -2, 9
	case "brotli":
		min, max = 0, 11
	case "lz4":
		min, max = 0, 9
	case "snappy", "uncompressed":
		return nil, fmt.Errorf("%s compression does not support a level", codec)
	default:
		return nil, fmt.Errorf("unsupported compression codec %q (supported: uncompressed, snappy, gzip, brotli, zstd, lz4)", codec)
	}

	switch name {
	case "normal":
		return nil, nil
	case "fast":
		return &min, nil
	case "best":
		return &max, nil
	default:
		return nil, fmt.Errorf("unknown compression level %q (want an integer or one of: fast, normal, best)", name)
	}
}

// ParseLevel parses a --compression-level flag value: either an integer
// literal or one of the named aliases "fast", "normal", "best" (see
// levelAlias), resolved against codec.
func ParseLevel(codec, raw string) (*int, error) {
	switch raw {
	case "fast", "normal", "best":
		return levelAlias(codec, raw)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid compression level %q (want an integer or one of: fast, normal, best)", raw)
	}
	return &n, nil
}

// Resolve merges CLI-flag and config-file settings, field by field, with
// cli taking priority over file, and file over the built-in default
// (DefaultCodec at its codec's default level).
func Resolve(cli, file Settings) Settings {
	resolved := Settings{Codec: DefaultCodec}
	if file.Codec != "" {
		resolved.Codec = file.Codec
	}
	if file.Level != nil {
		resolved.Level = file.Level
	}
	if cli.Codec != "" {
		resolved.Codec = cli.Codec
	}
	if cli.Level != nil {
		resolved.Level = cli.Level
	}
	return resolved
}

// Validate checks that Codec is a known name and, if Level is set, that it
// falls within the range that codec accepts.
func (s Settings) Validate() error {
	switch s.Codec {
	case "", "zstd":
		if s.Level != nil && (*s.Level < 1 || *s.Level > 4) {
			return fmt.Errorf("zstd compression level must be 1-4 (1=fastest .. 4=best compression), got %d", *s.Level)
		}
	case "gzip":
		if s.Level != nil && (*s.Level < -2 || *s.Level > 9) {
			return fmt.Errorf("gzip compression level must be -2-9 (-2=Huffman-only, -1=default, 0=none, 9=best), got %d", *s.Level)
		}
	case "brotli":
		if s.Level != nil && (*s.Level < 0 || *s.Level > 11) {
			return fmt.Errorf("brotli compression level must be 0-11, got %d", *s.Level)
		}
	case "lz4":
		if s.Level != nil && (*s.Level < 0 || *s.Level > 9) {
			return fmt.Errorf("lz4 compression level must be 0-9 (0=fast, 1-9=high compression), got %d", *s.Level)
		}
	case "snappy", "uncompressed":
		if s.Level != nil {
			return fmt.Errorf("%s compression does not support a level", s.Codec)
		}
	default:
		return fmt.Errorf("unsupported compression codec %q (supported: uncompressed, snappy, gzip, brotli, zstd, lz4)", s.Codec)
	}
	return nil
}

// WriterOption builds the parquet.WriterOption that applies these settings.
// Callers must call Validate first; WriterOption does not re-validate Level
// and falls back to the codec's default level if Level is nil.
func (s Settings) WriterOption() parquet.WriterOption {
	return parquet.Compression(s.CodecInstance())
}

// CodecInstance builds the compress.Codec these settings select. It is exported for
// callers (like pqcopy) that need to force a codec onto a schema's leaf
// nodes directly, bypassing a node's own recorded compression; most callers
// should use WriterOption instead.
func (s Settings) CodecInstance() compress.Codec {
	switch s.Codec {
	case "uncompressed":
		return &uncompressed.Codec{}
	case "snappy":
		return &snappy.Codec{}
	case "gzip":
		codec := gzip.Codec{Level: gzip.DefaultCompression}
		if s.Level != nil {
			codec.Level = *s.Level
		}
		return &codec
	case "brotli":
		codec := brotli.Codec{Quality: brotli.DefaultQuality, LGWin: brotli.DefaultLGWin}
		if s.Level != nil {
			codec.Quality = *s.Level
		}
		return &codec
	case "lz4":
		codec := lz4.Codec{Level: lz4.DefaultLevel}
		if s.Level != nil {
			codec.Level = lz4Levels[*s.Level]
		}
		return &codec
	default: // "", "zstd"
		codec := zstd.Codec{Level: zstd.DefaultLevel}
		if s.Level != nil {
			codec.Level = zstd.Level(*s.Level)
		}
		return &codec
	}
}
