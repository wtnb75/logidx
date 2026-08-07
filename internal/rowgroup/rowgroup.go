// Package rowgroup selects the Parquet row group row-count limit used when
// writing output files, and resolves that choice from CLI flags, the rules
// config file, and a built-in default (unlimited — parquet-go's own
// default), in that priority order.
package rowgroup

import (
	"fmt"

	"github.com/parquet-go/parquet-go"
)

// Settings selects a Parquet row group row-count limit. The zero value
// means "unset" (MaxRows == nil), used as an input to Resolve. Unlike
// compression.Settings, Resolve does not force this to a non-nil default:
// unset stays unset, meaning unlimited row group size (parquet-go's own
// default), so existing output file structure doesn't silently change for
// callers that never configure this.
type Settings struct {
	MaxRows *int64 `yaml:"max_rows" json:"max_rows,omitempty"`
}

// Resolve merges CLI-flag and config-file settings, with cli taking
// priority over file. There is no built-in non-nil default: if neither
// sets MaxRows, the result leaves it nil.
func Resolve(cli, file Settings) Settings {
	resolved := Settings{}
	if file.MaxRows != nil {
		resolved.MaxRows = file.MaxRows
	}
	if cli.MaxRows != nil {
		resolved.MaxRows = cli.MaxRows
	}
	return resolved
}

// Validate checks that MaxRows, if set, is positive.
func (s Settings) Validate() error {
	if s.MaxRows != nil && *s.MaxRows <= 0 {
		return fmt.Errorf("row group max_rows must be positive, got %d", *s.MaxRows)
	}
	return nil
}

// Option returns the parquet.WriterOption that applies this row group
// limit, and whether one applies at all. Callers must call Validate first.
// When MaxRows is nil (unset), ok is false and callers should not add any
// option — parquet-go's own default (unlimited) applies.
func (s Settings) Option() (option parquet.WriterOption, ok bool) {
	if s.MaxRows == nil {
		return nil, false
	}
	return parquet.MaxRowsPerRowGroup(*s.MaxRows), true
}
