package parse

import (
	"time"

	"logidx/internal/rules"
)

// Match tries each rule in ruleList in order and returns the extracted,
// type-converted field values of the first rule whose pattern matches line.
// If that rule's pattern matches but any field fails type conversion, the
// line is treated as unmatched (ok=false) — there is no fallthrough to
// subsequent rules, since "first match" refers to the regex match, not to
// conversion success.
func Match(ruleList []rules.Rule, line string, now time.Time) (name string, values map[string]any, ok bool) {
	for _, rule := range ruleList {
		m := rule.Regexp.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		raw := map[string]string{}
		for i, groupName := range rule.Regexp.SubexpNames() {
			if i == 0 || groupName == "" {
				continue
			}
			raw[groupName] = m[i]
		}

		converted := make(map[string]any, len(rule.Fields))
		for fieldName, field := range rule.Fields {
			v, err := convertValue(raw[fieldName], field, now)
			if err != nil {
				return "", nil, false
			}
			converted[fieldName] = v
		}

		return rule.Name, converted, true
	}

	return "", nil, false
}
