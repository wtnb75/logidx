package rules

import (
	"bytes"
	"regexp/syntax"
	"slices"

	"gopkg.in/yaml.v3"
)

// findKeyIndex returns the flat Content index of key's key node within
// mapping (a yaml.Node of Kind MappingNode), or -1 if mapping has no such
// key. mapping.Content alternates key, value, key, value... in document
// order regardless of key name, so the paired value node is always at the
// returned index + 1.
func findKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// findRulesSequence returns the `rules:` sequence node of a parsed rules
// YAML document, or nil if the document has no rules: key (a valid,
// rules-less config with nothing to convert) or no content at all (an
// empty document).
func findRulesSequence(doc *yaml.Node) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	idx := findKeyIndex(root, "rules")
	if idx < 0 {
		return nil
	}
	return root.Content[idx+1]
}

// marshalDoc re-serializes doc with 2-space indentation, matching this
// repo's rules.yaml convention (see sampleRulesYAML in rules_test.go).
// Plain yaml.Marshal defaults to 4-space indent, which would silently
// reindent every line of an unmodified rules.yaml - this is why
// Expand/Collapse don't call it directly.
func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalizePattern parses pattern with regexp/syntax and returns the
// canonical form of its parse tree (e.g. "\/" -> "/", reordered
// character-class ranges). Plain regexp.Regexp.String() can't be used for
// this: per its doc comment it "returns the source text used to compile
// the regular expression" - i.e. the verbatim input - so two source
// strings that compile to the same regexp but are spelled differently
// would never compare equal. The canonical form isn't meant to be read by
// humans (^ becomes \A, the whole thing gets wrapped in (?-m:...)) and is
// only ever used for equality checks in Collapse - see matchingPreset.
func normalizePattern(pattern string) (string, error) {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", err
	}
	return re.String(), nil
}

// sortedPresetNames returns presetRegistry's keys sorted, so Collapse
// scans presets in a deterministic order when deciding which preset a rule
// matches (see the design doc's note on multiple matches).
func sortedPresetNames() []string {
	names := make([]string, 0, len(presetRegistry))
	for name := range presetRegistry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
