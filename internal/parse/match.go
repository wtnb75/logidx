package parse

import (
	"encoding/json"
	"fmt"
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
// definitions. If rule.Structured is set, raw[rule.Structured.Source] is
// first parsed into a flat key/value map (see ParseStructured); fields with
// Key set pull their raw value from that map instead of from raw, and the
// field with Extra set (if any) receives a JSON object of every structured
// key not consumed by a Key field. Returns an error if any field fails
// conversion - callers treat that the same way a failed match is treated
// (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	if rule.Structured != nil {
		structuredValues, err = ParseStructured(rule.Structured.Format, raw[rule.Structured.Source])
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil {
		extraJSON, err = marshalUnconsumed(rule.Fields, structuredValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
		case field.Extra:
			rawValue = extraJSON
		case field.Key != "":
			rawValue = structuredValues[field.Key]
		}
		v, err := convertValue(rawValue, field, now)
		if err != nil {
			return nil, err
		}
		converted[field.Name] = v
	}
	return converted, nil
}

// marshalUnconsumed collects every key in structuredValues not consumed by
// a field's Key, and marshals the remainder as a JSON object. json.Marshal
// always sorts map keys, so the result is deterministic across runs.
func marshalUnconsumed(fields []rules.Field, structuredValues map[string]string) (string, error) {
	consumed := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.Key != "" {
			consumed[f.Key] = true
		}
	}

	remaining := make(map[string]string, len(structuredValues))
	for k, v := range structuredValues {
		if !consumed[k] {
			remaining[k] = v
		}
	}

	b, err := json.Marshal(remaining)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
