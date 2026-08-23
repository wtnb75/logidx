package rules

import (
	"strings"
	"unicode/utf8"
)

// Reason returns "" when line should not be ignored, otherwise the name of
// the first matching condition, checked in this fixed priority order
// regardless of declaration order in rules.yaml: "empty", "invalid_utf8",
// "max_length", "pattern". The order is an implementation choice (cheapest
// checks first) - see the ignore: design doc.
func (ic *IgnoreConfig) Reason(line string) string {
	if ic.Empty && strings.TrimSpace(line) == "" {
		return "empty"
	}
	if ic.InvalidUTF8 && !utf8.ValidString(line) {
		return "invalid_utf8"
	}
	if ic.MaxLength > 0 && len(line) > ic.MaxLength {
		return "max_length"
	}
	for _, re := range ic.PatternsRe {
		if re.MatchString(line) {
			return "pattern"
		}
	}
	return ""
}
