package parse

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ParseStructured parses raw (the captured substring named by a rule's
// Structured.Source) according to format ("json", "ltsv", or "logfmt") into
// a flat map of key to string value. Nested JSON objects/arrays are
// re-encoded as their own compact JSON string; JSON numbers keep their
// original textual digits (via json.Number, avoiding float64 formatting
// artifacts); JSON null becomes an empty string. LTSV/logfmt values are
// already flat strings and pass through unchanged. Returns an error if raw
// isn't valid for the given format.
func ParseStructured(format, raw string) (map[string]string, error) {
	switch format {
	case "json":
		return parseStructuredJSON(raw)
	case "ltsv":
		return parseStructuredLTSV(raw)
	case "logfmt":
		return parseStructuredLogfmt(raw)
	default:
		return nil, fmt.Errorf("unsupported structured format %q", format)
	}
}

// ParsePreset runs re (a preset's compiled pattern, from
// rules.StructuredConfig.PresetRegexp) against raw and returns its named
// capture groups as a flat map, the same shape ParseStructured returns for
// json/ltsv/logfmt - so callers (see Convert) can treat a preset-format
// structured source identically to json/ltsv/logfmt once parsed. Returns an
// error if raw doesn't match re.
func ParsePreset(re *regexp.Regexp, raw string) (map[string]string, error) {
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("preset pattern did not match structured source: %q", raw)
	}

	result := make(map[string]string, len(m))
	for i, name := range re.SubexpNames() {
		if i == 0 || name == "" {
			continue
		}
		result[name] = m[i]
	}
	return result, nil
}

func parseStructuredJSON(raw string) (map[string]string, error) {
	values, _, err := parseStructuredJSONTyped(raw)
	return values, err
}

// parseStructuredJSONTyped decodes raw the same way parseStructuredJSON
// does, but returns both the flattened string map used by Key-addressed
// fields and a map preserving each value's original JSON type (json.Number,
// bool, nil, nested map[string]any/[]any). The typed map lets Extra
// remarshal unconsumed keys without losing their original JSON shape - see
// marshalUnconsumed in match.go.
func parseStructuredJSONTyped(raw string) (values map[string]string, typed map[string]any, err error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	var top map[string]any
	if err := dec.Decode(&top); err != nil {
		return nil, nil, fmt.Errorf("decode json: %w", err)
	}
	if top == nil {
		// A top-level JSON `null` decodes into a nil map with err == nil
		// (Go's documented behavior for unmarshaling null into a map) - it
		// is not an object, so it must be rejected explicitly.
		return nil, nil, fmt.Errorf("decode json: top-level value must be an object, got null")
	}
	if dec.More() {
		// Decoder.Decode only consumes one JSON value; trailing bytes
		// (garbage, or a second concatenated value) must be rejected too.
		return nil, nil, fmt.Errorf("decode json: unexpected trailing data after top-level value")
	}

	values = make(map[string]string, len(top))
	for k, v := range top {
		s, err := jsonValueToString(v)
		if err != nil {
			return nil, nil, fmt.Errorf("encode json field %q: %w", k, err)
		}
		values[k] = s
	}
	return values, top, nil
}

func jsonValueToString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	default:
		// object or array: re-encode as compact JSON.
		compact, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(compact), nil
	}
}

func parseStructuredLTSV(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("ltsv: empty input")
	}
	result := map[string]string{}
	for field := range strings.SplitSeq(raw, "\t") {
		key, value, found := strings.Cut(field, ":")
		if !found {
			continue
		}
		result[key] = value
	}
	return result, nil
}

func parseStructuredLogfmt(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, fmt.Errorf("logfmt: empty input")
	}
	result := map[string]string{}
	i, n := 0, len(raw)
	for i < n {
		for i < n && raw[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}

		start := i
		for i < n && raw[i] != '=' && raw[i] != ' ' {
			i++
		}
		key := raw[start:i]
		if key == "" {
			return nil, fmt.Errorf("logfmt: unexpected %q at position %d", raw[i], i)
		}
		if i >= n || raw[i] != '=' {
			result[key] = ""
			continue
		}
		i++ // skip '='

		if i < n && raw[i] == '"' {
			start := i
			i++
			closed := false
			for i < n {
				c := raw[i]
				if c == '\\' && i+1 < n {
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("logfmt: unterminated quoted value for key %q", key)
			}
			unquoted, err := strconv.Unquote(raw[start:i])
			if err != nil {
				return nil, fmt.Errorf("logfmt: invalid quoted value for key %q: %w", key, err)
			}
			result[key] = unquoted
			continue
		}

		start = i
		for i < n && raw[i] != ' ' {
			i++
		}
		result[key] = raw[start:i]
	}
	return result, nil
}
