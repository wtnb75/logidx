package parse

import "github.com/wtnb75/logidx/internal/rules"

// applyNormalize tries each normalize rule in order and returns the value
// of the first one whose pattern matches raw. If none match, raw is
// returned unchanged.
func applyNormalize(raw string, rulesList []rules.NormalizeRule) string {
	for _, r := range rulesList {
		if r.Regexp.MatchString(raw) {
			return r.Value
		}
	}
	return raw
}
