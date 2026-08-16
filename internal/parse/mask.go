package parse

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/wtnb75/logidx/internal/rules"
)

// SplitMaskRules partitions mask (Config.Mask, in declaration order) into
// its key-targeting and pattern-targeting subsets, preserving each
// subset's relative order - callers apply each subset as its own chain
// (see applyKeyMaskJSON/applyKeyMaskFlat/ApplyPatternMask).
func SplitMaskRules(mask []rules.MaskRule) (keyRules, patternRules []rules.MaskRule) {
	for _, m := range mask {
		switch m.Type {
		case "key":
			keyRules = append(keyRules, m)
		case "pattern":
			patternRules = append(patternRules, m)
		}
	}
	return keyRules, patternRules
}

// applyKeyMaskJSON walks tree (a JSON value decoded via encoding/json with
// UseNumber(): map[string]any, []any, json.Number, string, bool, or nil)
// recursively at any depth. Every map key matching a keyRules pattern has
// its value replaced in place by the chained mask result (see
// maskKeyIfMatched); values under non-matching keys, and every array
// element, are recursed into instead.
//
// When keyRules is empty (mask: unset, or no type: key rules configured)
// this returns immediately without walking tree at all, so the "mask:
// unset is a no-op" guarantee holds with no extra cost on the hot import
// path - see maskKeyIfMatched for the matching per-key short-circuit.
func applyKeyMaskJSON(tree any, keyRules []rules.MaskRule) {
	if len(keyRules) == 0 {
		return
	}
	switch t := tree.(type) {
	case map[string]any:
		for k, v := range t {
			if masked, matched := maskKeyIfMatched(k, v, keyRules); matched {
				t[k] = masked
				continue
			}
			applyKeyMaskJSON(v, keyRules)
		}
	case []any:
		for _, v := range t {
			applyKeyMaskJSON(v, keyRules)
		}
	}
}

// applyKeyMaskFlat is applyKeyMaskJSON's flat-map counterpart for LTSV/
// logfmt structured data, which has no nesting to recurse into: every
// top-level key matching a keyRules pattern is masked in place.
//
// When keyRules is empty this returns immediately without iterating m -
// see applyKeyMaskJSON.
func applyKeyMaskFlat(m map[string]string, keyRules []rules.MaskRule) {
	if len(keyRules) == 0 {
		return
	}
	for k, v := range m {
		if masked, matched := maskKeyIfMatched(k, v, keyRules); matched {
			m[k] = masked
		}
	}
}

// maskKeyIfMatched checks key against every keyRules pattern first (a cheap
// regexp match against the key name only) and, only once it knows at least
// one rule will actually fire, stringifies v (jsonValueToString - the same
// conversion structured.go's json path already uses for untouched values)
// and chains every matching rule's action over it in declaration order (a
// later matching rule's action input is the earlier one's output). matched
// is false, and masked is unspecified, when no rule matches key - v is
// never stringified in that case, and callers must check matched before
// using masked.
func maskKeyIfMatched(key string, v any, keyRules []rules.MaskRule) (masked string, matched bool) {
	for _, rule := range keyRules {
		if !rule.Regexp.MatchString(key) {
			continue
		}
		if !matched {
			matched = true
			s, err := jsonValueToString(v)
			if err != nil {
				s = ""
			}
			masked = s
		}
		masked = applyMaskAction(masked, rule)
	}
	return masked, matched
}

// ApplyPatternMask chains every patternRules entry over s in declaration
// order, replacing each rule's regexp matches with its masked action
// result - action: redact substitutes the whole match with rule.Value
// (Go's regexp.ReplaceAllString, so $1-style backreferences work);
// action: hash replaces each match independently with its own truncated
// SHA-256 digest (see hashTrunc).
func ApplyPatternMask(s string, patternRules []rules.MaskRule) string {
	for _, rule := range patternRules {
		switch rule.Action {
		case "redact":
			s = rule.Regexp.ReplaceAllString(s, rule.Value)
		case "hash":
			s = rule.Regexp.ReplaceAllStringFunc(s, func(match string) string {
				return hashTrunc(match, rule.Length)
			})
		}
	}
	return s
}

// applyMaskAction applies one MaskRule's action (already validated to be
// "redact" or "hash" by Config.Validate - see internal/rules/validate.go)
// to s, the already-matched value or substring being masked.
func applyMaskAction(s string, rule rules.MaskRule) string {
	if rule.Action == "hash" {
		return hashTrunc(s, rule.Length)
	}
	return rule.Value
}

// hashTrunc returns the first length hex characters (length is 1-64,
// enforced by Config.Validate) of s's SHA-256 digest. No secret key is
// used deliberately: the same input must always produce the same output,
// so masked values stay correlatable across rows without revealing the
// original value - see the design's Non-goals on HMAC.
func hashTrunc(s string, length int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:length]
}
