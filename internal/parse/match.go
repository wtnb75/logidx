package parse

import (
	"time"

	"logidx/internal/rules"
)

// MatchRaw tries each rule's pattern against line and returns the first
// match's rule and raw (un-type-converted) captured field values, keyed by
// field name. No type conversion happens here - see Convert.
func MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		m := r.Regexp.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		captured := map[string]string{}
		for j, groupName := range r.Regexp.SubexpNames() {
			if j == 0 || groupName == "" {
				continue
			}
			captured[groupName] = m[j]
		}
		return r, captured, true
	}
	return nil, nil, false
}

// Convert type-converts raw's captured values according to rule's field
// definitions. Returns an error if any field fails conversion - callers
// treat that the same way a failed match is treated (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		v, err := convertValue(raw[field.Name], field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}

// Match tries each rule in ruleList in order and returns the extracted,
// type-converted field values of the first rule whose pattern matches line.
// If that rule's pattern matches but any field fails type conversion, the
// line is treated as unmatched (ok=false) — there is no fallthrough to
// subsequent rules, since "first match" refers to the regex match, not to
// conversion success. Match is a thin wrapper of MatchRaw+Convert, kept for
// callers (and tests) that don't need the two-stage split.
func Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool) {
	rule, raw, matched := MatchRaw(ruleList, line)
	if !matched {
		return "", nil, false
	}
	values, err := Convert(*rule, raw, now)
	if err != nil {
		return "", nil, false
	}
	return rule.Name, values, true
}
