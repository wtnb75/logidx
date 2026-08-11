package rules

import (
	"bytes"
	"fmt"
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

// fieldsEqual reports whether a and b are identical for every attribute a
// preset definition can set (Name, Type, Format, Key, Extra, Replace,
// Normalize), element-for-element in order. Deliberately excludes Meta,
// ResolvedFormat, and the compiled Regexp inside Replace/Normalize
// entries: Meta is never set by a preset definition (see the design doc's
// collapse section), and the other two are derived at Load time, not part
// of the YAML declaration being compared.
func fieldsEqual(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Type != b[i].Type ||
			a[i].Format != b[i].Format ||
			a[i].Key != b[i].Key ||
			a[i].Extra != b[i].Extra {
			return false
		}
		if !replaceRulesEqual(a[i].Replace, b[i].Replace) {
			return false
		}
		if !normalizeRulesEqual(a[i].Normalize, b[i].Normalize) {
			return false
		}
	}
	return true
}

func replaceRulesEqual(a, b []ReplaceRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Pattern != b[i].Pattern || a[i].Replacement != b[i].Replacement {
			return false
		}
	}
	return true
}

func normalizeRulesEqual(a, b []NormalizeRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Pattern != b[i].Pattern || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func boolNode(value bool) *yaml.Node {
	v := "false"
	if value {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func appendKV(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content, scalarNode(key), value)
}

// fieldUsesOnlyShorthand reports whether f can be written as the `name:
// type` scalar shorthand: none of the attributes that force the full
// mapping form are set. Mirrors Field.UnmarshalYAML's two accepted forms
// (rules.go) in reverse.
func fieldUsesOnlyShorthand(f Field) bool {
	return f.Format == "" && f.Key == "" && !f.Extra && len(f.Replace) == 0 && len(f.Normalize) == 0
}

// encodeFieldNode renders f as a yaml.Node: the shorthand scalar form when
// possible, otherwise the full mapping form listing only the attributes f
// actually sets. Generic over every Field attribute so it keeps working if
// a future preset uses replace/normalize/key/extra, even though today's
// presets only use type/format.
func encodeFieldNode(f Field) *yaml.Node {
	if fieldUsesOnlyShorthand(f) {
		return scalarNode(f.Type)
	}

	mapping := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(mapping, "type", scalarNode(f.Type))
	if f.Format != "" {
		appendKV(mapping, "format", scalarNode(f.Format))
	}
	if f.Key != "" {
		appendKV(mapping, "key", scalarNode(f.Key))
	}
	if f.Extra {
		appendKV(mapping, "extra", boolNode(true))
	}
	if len(f.Replace) > 0 {
		appendKV(mapping, "replace", encodeReplaceRulesNode(f.Replace))
	}
	if len(f.Normalize) > 0 {
		appendKV(mapping, "normalize", encodeNormalizeRulesNode(f.Normalize))
	}
	return mapping
}

func encodeReplaceRulesNode(rules []ReplaceRule) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, r := range rules {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		appendKV(entry, "pattern", scalarNode(r.Pattern))
		appendKV(entry, "value", scalarNode(r.Replacement))
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

func encodeNormalizeRulesNode(rules []NormalizeRule) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, r := range rules {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		appendKV(entry, "pattern", scalarNode(r.Pattern))
		appendKV(entry, "value", scalarNode(r.Value))
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

// encodeFieldsNode renders fields as a `fields:` mapping node's value, in
// declaration order.
func encodeFieldsNode(fields []Field) *yaml.Node {
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	for _, f := range fields {
		appendKV(mapping, f.Name, encodeFieldNode(f))
	}
	return mapping
}

// Expand rewrites every rule's `preset:` into the pattern/fields it names,
// leaving everything else in data byte-for-byte where possible (comments,
// key order, indentation, non-preset rules). Returns the rewritten YAML and
// the number of rules it expanded.
func Expand(data []byte) ([]byte, int, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
	}
	if doc.Kind == 0 {
		// Empty input decodes to a zero Node - nothing to walk, and
		// re-marshaling a zero Node produces "null\n" instead of "",
		// which would be a spurious change to a genuinely empty file.
		if _, err := loadConfig(data); err != nil {
			return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
		}
		return data, 0, nil
	}

	rulesSeq := findRulesSequence(&doc)
	count := 0
	if rulesSeq != nil {
		for _, ruleNode := range rulesSeq.Content {
			presetIdx := findKeyIndex(ruleNode, "preset")
			if presetIdx < 0 {
				continue
			}

			presetName := ruleNode.Content[presetIdx+1].Value
			preset, ok := presetRegistry[presetName]
			if !ok {
				name := ""
				if nameIdx := findKeyIndex(ruleNode, "name"); nameIdx >= 0 {
					name = ruleNode.Content[nameIdx+1].Value
				}
				return nil, 0, fmt.Errorf("rule %q: unknown preset %q", name, presetName)
			}

			patternKey := scalarNode("pattern")
			patternKey.HeadComment = ruleNode.Content[presetIdx].HeadComment
			patternKey.LineComment = ruleNode.Content[presetIdx].LineComment
			patternValue := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.SingleQuotedStyle, Value: preset.Pattern}

			fieldsKey := scalarNode("fields")
			fieldsValue := encodeFieldsNode(preset.Fields)

			newContent := make([]*yaml.Node, 0, len(ruleNode.Content)+2)
			for i := 0; i+1 < len(ruleNode.Content); i += 2 {
				if i == presetIdx {
					newContent = append(newContent, patternKey, patternValue, fieldsKey, fieldsValue)
					continue
				}
				newContent = append(newContent, ruleNode.Content[i], ruleNode.Content[i+1])
			}
			ruleNode.Content = newContent
			count++
		}
	}

	out, err := marshalDoc(&doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal expanded rules: %w", err)
	}
	if _, err := loadConfig(out); err != nil {
		return nil, 0, fmt.Errorf("expanded rules failed validation (this is a bug): %w", err)
	}
	return out, count, nil
}
