package rules

import (
	"bytes"
	"fmt"
	"regexp/syntax"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// collapseTrailingComment reconstructs any standalone comment(s) that were
// attached within the pattern:/fields: block being folded into a single
// preset: <name> line, so they survive instead of being silently dropped
// along with the discarded fields: node. Empirically, yaml.v3 attaches a
// standalone comment between pattern: and fields: to the fields KEY's
// HeadComment, and a standalone comment trailing the whole block to either
// the fields KEY's own FootComment (fields: is the rule's last key) or to
// the last field entry's KEY FootComment nested inside the fields:
// submapping (fields: is followed by another rule) - both shapes verified
// against real yaml.v3 output. Every candidate slot is checked; slots that
// don't apply are empty and contribute nothing. Comments are joined in
// source order.
// deepestLastNode descends into node's last child (last value of a mapping,
// last element of a sequence) until it reaches a leaf, and returns that
// leaf. Used to find where to attach a trailing FootComment so yaml.v3's
// encoder is still at the right indentation when it flushes the comment -
// see the call site in Expand.
func deepestLastNode(node *yaml.Node) *yaml.Node {
	switch {
	case node.Kind == yaml.MappingNode && len(node.Content) >= 2:
		return deepestLastNode(node.Content[len(node.Content)-1])
	case node.Kind == yaml.SequenceNode && len(node.Content) >= 1:
		return deepestLastNode(node.Content[len(node.Content)-1])
	default:
		return node
	}
}

func collapseTrailingComment(ruleNode *yaml.Node, patIdx, fieldsIdx int) string {
	candidates := []string{
		ruleNode.Content[patIdx].FootComment,
		ruleNode.Content[patIdx+1].FootComment,
		ruleNode.Content[fieldsIdx].HeadComment,
		ruleNode.Content[fieldsIdx].FootComment,
	}
	if fieldsValue := ruleNode.Content[fieldsIdx+1]; len(fieldsValue.Content) >= 2 {
		n := len(fieldsValue.Content)
		candidates = append(candidates, fieldsValue.Content[n-2].FootComment, fieldsValue.Content[n-1].FootComment)
	}
	var parts []string
	for _, c := range candidates {
		if c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n")
}

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
// rules-less config with nothing to convert), no content at all (an empty
// document), or rules: is itself a YAML alias/anchor reference rather than a
// literal sequence (Kind is AliasNode, not SequenceNode, in that case) -
// walking the raw node tree positionally against the alias-resolved
// *Config below only makes sense when rules: is a literal sequence.
func findRulesSequence(doc *yaml.Node) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	idx := findKeyIndex(root, "rules")
	if idx < 0 {
		return nil
	}
	seq := root.Content[idx+1]
	if seq.Kind != yaml.SequenceNode {
		return nil
	}
	return seq
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
// preset definition can set (Name, Type, Format, Key, Extra, Meta, Replace,
// Normalize), element-for-element in order. Meta is included even though no
// preset definition sets it today: it's a real declared YAML attribute (see
// Field.Meta in rules.go), and comparing it here means a rule that adds
// meta: to an otherwise preset-matching field is correctly never collapsed,
// instead of silently losing that attribute (see the design doc's collapse
// section for why data-loss here would be worse than under-collapsing).
// Deliberately excludes ResolvedFormat and the compiled Regexp inside
// Replace/Normalize entries: those are derived at Load time, not part of
// the YAML declaration being compared.
func fieldsEqual(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Type != b[i].Type ||
			a[i].Format != b[i].Format ||
			a[i].Key != b[i].Key ||
			a[i].Extra != b[i].Extra ||
			a[i].Meta != b[i].Meta {
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
	return f.Format == "" && f.Key == "" && !f.Extra && f.Meta == "" && len(f.Replace) == 0 && len(f.Normalize) == 0
}

// encodeFieldNode renders f as a yaml.Node: the shorthand scalar form when
// possible, otherwise the full mapping form listing only the attributes f
// actually sets. Generic over every attribute a preset can currently
// define (type/format/key/extra/meta/replace/normalize), so it keeps
// working if a future preset uses one it doesn't today.
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
	if f.Meta != "" {
		appendKV(mapping, "meta", scalarNode(f.Meta))
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
	// Validate the input up front, mirroring Collapse below, so a
	// pre-existing error in the user's own YAML (bad regexp, unknown
	// type, missing capture group, ...) that has nothing to do with any
	// preset: rewrite is reported as a normal, accurate error - not
	// wrapped in the "(this is a bug)" phrasing reserved for the
	// output-validation call at the end of this function.
	if _, err := loadConfig(data); err != nil {
		return nil, 0, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
	}
	if doc.Kind == 0 {
		// Empty input decodes to a zero Node - nothing to walk, and
		// re-marshaling a zero Node produces "null\n" instead of "",
		// which would be a spurious change to a genuinely empty file.
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
			patternValue := &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.SingleQuotedStyle, Value: preset.Pattern}
			// A trailing same-line "# comment" after `preset: name` attaches
			// to the VALUE node's LineComment in yaml.v3's node model, not
			// the key's (the key's LineComment is always empty) - so it
			// must be copied from/to the value nodes, not the key nodes.
			patternValue.LineComment = ruleNode.Content[presetIdx+1].LineComment

			fieldsKey := scalarNode("fields")
			fieldsValue := encodeFieldsNode(preset.Fields)
			// A standalone comment trailing `preset: name` attaches to the
			// preset KEY's FootComment (verified empirically). It must land
			// on the deepest last scalar leaf of the new fields: block, not
			// on fieldsValue itself (a MappingNode): yaml.v3 dedents a
			// FootComment attached to a mapping/sequence node to the
			// encoder's position after popping back out of that node's
			// children, which prints it at the wrong indentation (verified
			// empirically) - attaching it to the actual last leaf keeps the
			// encoder's position correct when it flushes the comment.
			deepestLastNode(fieldsValue).FootComment = ruleNode.Content[presetIdx].FootComment

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

	if count == 0 {
		// Nothing was rewritten - return data as-is rather than
		// re-serializing the whole node tree through the encoder, which
		// would silently reformat untouched YAML (e.g. collapse blank
		// lines between rules) even though this call converted nothing.
		// Input validation already happened above via loadConfig(data).
		return data, 0, nil
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

// Collapse rewrites every rule whose pattern/fields exactly match a
// registered preset (after normalization, see normalizePattern) into
// `preset: <name>`, leaving everything else untouched. Returns the
// rewritten YAML and the number of rules it collapsed.
func Collapse(data []byte) ([]byte, int, error) {
	cfg, err := loadConfig(data)
	if err != nil {
		return nil, 0, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("parse rules YAML: %w", err)
	}
	if doc.Kind == 0 {
		return data, 0, nil
	}

	rulesSeq := findRulesSequence(&doc)
	count := 0
	if rulesSeq != nil {
		for i, rule := range cfg.Rules {
			if rule.Preset != "" || !rule.declaredPatternOrFields {
				continue
			}

			presetName, matched, err := matchingPreset(rule)
			if err != nil {
				return nil, 0, err
			}
			if !matched {
				continue
			}

			// cfg.Rules is alias/merge-resolved by the yaml decoder, but
			// rulesSeq.Content is the raw, unresolved node tree - the two
			// don't line up positionally when a rule came from an anchor
			// alias (`- *base`), a merge key (`<<: *base`), or the whole
			// rules: sequence itself being an alias (already excluded by
			// findRulesSequence's Kind check above, but len(Content) can
			// still legitimately be shorter than len(cfg.Rules) in other
			// alias shapes). Skip anything that doesn't look like a
			// literal, directly-declared rule mapping rather than crash -
			// the safe, conservative behavior for YAML this project
			// already accepts via Load/logidx import.
			if i >= len(rulesSeq.Content) {
				continue
			}
			ruleNode := rulesSeq.Content[i]
			if ruleNode.Kind != yaml.MappingNode {
				continue
			}
			patIdx := findKeyIndex(ruleNode, "pattern")
			fieldsIdx := findKeyIndex(ruleNode, "fields")
			if patIdx < 0 {
				// rule.Pattern was set (declaredPatternOrFields is true)
				// but there's no literal pattern: key in the raw node -
				// e.g. it came in via a merge key. Nothing to rewrite
				// positionally.
				continue
			}

			presetKey := scalarNode("preset")
			presetKey.HeadComment = ruleNode.Content[patIdx].HeadComment
			presetValue := scalarNode(presetName)
			// A trailing same-line "# comment" after `pattern: '...'`
			// attaches to the VALUE node's LineComment in yaml.v3's node
			// model, not the key's (the key's LineComment is always
			// empty) - see the matching note in Expand above.
			presetValue.LineComment = ruleNode.Content[patIdx+1].LineComment
			// Any standalone comment inside the pattern:/fields: block
			// being discarded (between pattern: and fields:, or trailing
			// after fields:) must be reconstructed here - see
			// collapseTrailingComment for the empirically-verified node
			// shapes it handles.
			if fieldsIdx >= 0 {
				presetValue.FootComment = collapseTrailingComment(ruleNode, patIdx, fieldsIdx)
			} else {
				presetValue.FootComment = ruleNode.Content[patIdx].FootComment
			}

			newContent := make([]*yaml.Node, 0, len(ruleNode.Content))
			for j := 0; j+1 < len(ruleNode.Content); j += 2 {
				switch j {
				case patIdx:
					newContent = append(newContent, presetKey, presetValue)
				case fieldsIdx:
					// dropped: folded into the preset: pair above
				default:
					newContent = append(newContent, ruleNode.Content[j], ruleNode.Content[j+1])
				}
			}
			ruleNode.Content = newContent
			count++
		}
	}

	if count == 0 {
		// Nothing was rewritten - return data as-is rather than
		// re-serializing the whole node tree through the encoder, which
		// would silently reformat untouched YAML (e.g. collapse blank
		// lines between rules) even though this call converted nothing.
		// Input validation already happened above via loadConfig(data).
		return data, 0, nil
	}

	out, err := marshalDoc(&doc)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal collapsed rules: %w", err)
	}
	if _, err := loadConfig(out); err != nil {
		return nil, 0, fmt.Errorf("collapsed rules failed validation (this is a bug): %w", err)
	}
	return out, count, nil
}

// matchingPreset reports the name of the first (sorted) preset whose
// Pattern and Fields exactly match rule's, or ok=false if none does.
func matchingPreset(rule Rule) (name string, ok bool, err error) {
	ruleNorm, err := normalizePattern(rule.Pattern)
	if err != nil {
		return "", false, fmt.Errorf("rule %q: %w", rule.Name, err)
	}

	for _, presetName := range sortedPresetNames() {
		preset := presetRegistry[presetName]
		presetNorm, err := normalizePattern(preset.Pattern)
		if err != nil {
			return "", false, fmt.Errorf("preset %q: %w", presetName, err)
		}
		if ruleNorm == presetNorm && fieldsEqual(rule.Fields, preset.Fields) {
			return presetName, true, nil
		}
	}
	return "", false, nil
}
