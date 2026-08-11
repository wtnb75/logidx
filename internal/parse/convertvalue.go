package parse

import (
	"fmt"
	"strconv"
	"time"

	"github.com/wtnb75/logidx/internal/rules"
)

// convertValue applies the replace chain (if configured), then normalization
// (if configured), then converts the resulting string into the Go value
// matching field.Type. Returns an error if the value cannot be converted, in
// which case the caller should treat the whole line as unmatched.
func convertValue(raw string, field rules.Field, now time.Time) (any, error) {
	replaced := raw
	for _, r := range field.Replace {
		replaced = r.Regexp.ReplaceAllString(replaced, r.Replacement)
	}

	normalized := replaced
	if len(field.Normalize) > 0 {
		normalized = applyNormalize(replaced, field.Normalize)
	}

	switch field.Type {
	case "string":
		return normalized, nil
	case "int":
		v, err := strconv.ParseInt(normalized, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse int: %w", err)
		}
		return v, nil
	case "float":
		v, err := strconv.ParseFloat(normalized, 64)
		if err != nil {
			return nil, fmt.Errorf("parse float: %w", err)
		}
		return v, nil
	case "timestamp":
		v, err := parseTimestamp(normalized, field.ResolvedFormat, now)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", field.Type)
	}
}
