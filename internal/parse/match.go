package parse

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/wtnb75/logidx/internal/rules"
)

// matchRule tries r's pattern against line and, if it matches, returns its
// named captures.
func matchRule(r *rules.Rule, line string) (raw map[string]string, matched bool) {
	m := r.Regexp.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	captured := map[string]string{}
	for j, groupName := range r.Regexp.SubexpNames() {
		if j == 0 || groupName == "" {
			continue
		}
		captured[groupName] = m[j]
	}
	return captured, true
}

// MatchRaw tries each rule's pattern against line and returns the first
// match's rule and raw (un-type-converted) captured field values, keyed by
// field name. No type conversion happens here - see Convert.
func MatchRaw(ruleList []rules.Rule, line string) (rule *rules.Rule, raw map[string]string, ok bool) {
	for i := range ruleList {
		r := &ruleList[i]
		if captured, matched := matchRule(r, line); matched {
			return r, captured, true
		}
	}
	return nil, nil, false
}

// SourceMeta carries the input file's path and the current line number,
// for fields declared with meta: source_file / meta: source_line (see
// rules.Field.Meta). File matches unmatched.txt's convention: "-" for
// stdin, otherwise the input path as given. Line is 1-based; for a
// continuation-merged entry it's the entry's first physical line, not
// whichever continuation line happens to be current.
type SourceMeta struct {
	File string
	Line int
}

// Convert type-converts raw's captured values according to rule's field
// definitions. If rule.Structured is set, raw[rule.Structured.Source] is
// first parsed into a flat key/value map (see ParseStructured); fields with
// Key set pull their raw value from that map instead of from raw, fields
// with Meta set pull their raw value from source instead (see
// rules.Field.Meta / SourceMeta), and the field with Extra set (if any)
// receives a JSON object of every structured key not consumed by a Key
// field. Returns an error if any field fails conversion - callers treat
// that the same way a failed match is treated (write to unmatched).
func Convert(rule rules.Rule, raw map[string]string, source SourceMeta, now time.Time) (values map[string]any, err error) {
	var structuredValues map[string]string
	// typedStructuredValues preserves each JSON value's original type
	// (number/bool/nested object or array), instead of the flattened
	// string form structuredValues uses - see marshalUnconsumed. Only
	// populated for the plain "json" format, since ltsv/logfmt/preset
	// values have no native type richer than a string to preserve.
	var typedStructuredValues map[string]any
	if rule.Structured != nil {
		structuredRaw := raw[rule.Structured.Source]
		switch {
		case rule.Structured.PresetRegexp != nil:
			structuredValues, err = ParsePreset(rule.Structured.PresetRegexp, structuredRaw)
		case rule.Structured.Format == "json":
			structuredValues, typedStructuredValues, err = parseStructuredJSONTyped(structuredRaw)
		default:
			structuredValues, err = ParseStructured(rule.Structured.Format, structuredRaw)
		}
		if err != nil {
			return nil, fmt.Errorf("parse structured data: %w", err)
		}
	}

	var extraJSON string
	if structuredValues != nil && slices.ContainsFunc(rule.Fields, func(f rules.Field) bool { return f.Extra }) {
		extraValues := typedStructuredValues
		if extraValues == nil {
			extraValues = stringMapToAny(structuredValues)
		}
		extraJSON, err = marshalUnconsumed(rule.Fields, extraValues)
		if err != nil {
			return nil, fmt.Errorf("encode extra field: %w", err)
		}
	}

	converted := make(map[string]any, len(rule.Fields))
	for _, field := range rule.Fields {
		rawValue := raw[field.Name]
		switch {
		case field.Meta == rules.FieldMetaSourceFile:
			rawValue = source.File
		case field.Meta == rules.FieldMetaSourceLine:
			rawValue = strconv.Itoa(source.Line)
		case field.Extra:
			rawValue = extraJSON
		case field.Key != "":
			v, ok := structuredValues[field.Key]
			if !ok {
				return nil, fmt.Errorf("structured data missing key %q", field.Key)
			}
			rawValue = v
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
// structuredValues' values carry whatever type the caller gave them: the
// plain "json" format passes each value's original JSON type (see
// Convert's typedStructuredValues), so a number/bool/nested object here
// remarshals as itself instead of a quoted string.
func marshalUnconsumed(fields []rules.Field, structuredValues map[string]any) (string, error) {
	consumed := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.Key != "" {
			consumed[f.Key] = true
		}
	}

	remaining := make(map[string]any, len(structuredValues))
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

// stringMapToAny wraps a map[string]string as a map[string]any with the
// same string values, so marshalUnconsumed's single map[string]any
// parameter also serves formats (ltsv, logfmt, presets) that never had
// richer-than-string type information to preserve in the first place.
func stringMapToAny(m map[string]string) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// MatchAttempt records one single-line rule whose pattern matched line but
// whose field conversion failed - see MatchAndConvert.
type MatchAttempt struct {
	RuleName string
	Err      error
}

// MatchAndConvertFrom is MatchAndConvert generalized to start scanning
// ruleList at startIndex instead of always at 0. ruleIndex reports the
// index (within ruleList) of the rule that ultimately matched - callers
// that later need to retry from "the next candidate after this one" (see
// internal/convert.fileCursor.finalizeEntry) pass ruleIndex+1 back in as
// startIndex. ruleIndex is -1 when ok is false.
func MatchAndConvertFrom(ruleList []rules.Rule, startIndex int, line string, source SourceMeta, now time.Time) (rule *rules.Rule, ruleIndex int, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	for i := startIndex; i < len(ruleList); i++ {
		r := &ruleList[i]
		captured, matched := matchRule(r, line)
		if !matched {
			continue
		}

		if r.ContinuationRegexp != nil {
			return r, i, captured, nil, attempts, true
		}

		v, err := Convert(*r, captured, source, now)
		if err != nil {
			attempts = append(attempts, MatchAttempt{RuleName: r.Name, Err: err})
			continue
		}
		return r, i, captured, v, attempts, true
	}
	return nil, -1, nil, nil, attempts, false
}

// MatchAndConvert tries every rule in ruleList, in declaration order - the
// startIndex=0 case of MatchAndConvertFrom. A non-continuation rule whose
// pattern matches is converted immediately; if conversion fails, that rule
// is treated as a non-match and the next candidate rule is tried -
// conversion failure no longer ends the search. A continuation rule whose
// pattern matches is returned right away without conversion (values ==
// nil): its entry accumulates further lines and is converted later by the
// caller (see internal/convert.fileCursor.finalizeEntry, which can now
// also fall back via MatchAndConvertFrom on a conversion failure there).
func MatchAndConvert(ruleList []rules.Rule, line string, source SourceMeta, now time.Time) (rule *rules.Rule, raw map[string]string, values map[string]any, attempts []MatchAttempt, ok bool) {
	rule, _, raw, values, attempts, ok = MatchAndConvertFrom(ruleList, 0, line, source, now)
	return
}
