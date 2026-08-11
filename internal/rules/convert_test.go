package rules

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizePattern_CollapsesRedundantEscape(t *testing.T) {
	a, err := normalizePattern(`^(?P<a>\/foo)$`)
	if err != nil {
		t.Fatalf("normalizePattern returned error: %v", err)
	}
	b, err := normalizePattern(`^(?P<a>/foo)$`)
	if err != nil {
		t.Fatalf("normalizePattern returned error: %v", err)
	}
	if a != b {
		t.Errorf("normalizePattern(%q) = %q, normalizePattern(%q) = %q, want equal", `^(?P<a>\/foo)$`, a, `^(?P<a>/foo)$`, b)
	}
}

func TestNormalizePattern_InvalidPatternIsError(t *testing.T) {
	if _, err := normalizePattern("(unclosed"); err == nil {
		t.Fatal("expected error for invalid pattern")
	}
}

func TestFindKeyIndex_FindsKeyAndReportsMissing(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("name: app_log\npattern: 'x'\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	mapping := doc.Content[0]

	if idx := findKeyIndex(mapping, "pattern"); idx != 2 {
		t.Errorf("findKeyIndex(pattern) = %d, want 2", idx)
	}
	if idx := findKeyIndex(mapping, "fields"); idx != -1 {
		t.Errorf("findKeyIndex(fields) = %d, want -1", idx)
	}
}

func TestFindRulesSequence_NoRulesKeyReturnsNil(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("compression:\n  codec: zstd\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if seq := findRulesSequence(&doc); seq != nil {
		t.Errorf("findRulesSequence = %v, want nil", seq)
	}
}

func TestFindRulesSequence_ReturnsSequenceNode(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("rules:\n  - name: a\n"), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	seq := findRulesSequence(&doc)
	if seq == nil {
		t.Fatal("findRulesSequence returned nil")
	}
	if seq.Kind != yaml.SequenceNode {
		t.Errorf("seq.Kind = %v, want SequenceNode", seq.Kind)
	}
	if len(seq.Content) != 1 {
		t.Errorf("len(seq.Content) = %d, want 1", len(seq.Content))
	}
}

func TestSortedPresetNames_ReturnsAllFourSorted(t *testing.T) {
	got := sortedPresetNames()
	want := []string{"apache_clf", "apache_combined", "syslog_rfc3164", "syslog_rfc5424"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMarshalDoc_PreservesTwoSpaceIndentAndComments(t *testing.T) {
	src := `# top comment
rules:
  - name: app_log # inline comment
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	out, err := marshalDoc(&doc)
	if err != nil {
		t.Fatalf("marshalDoc returned error: %v", err)
	}
	if string(out) != src {
		t.Errorf("marshalDoc round-trip changed output:\nwant:\n%s\ngot:\n%s", src, out)
	}
}

func TestFieldsEqual_IdenticalFieldsMatch(t *testing.T) {
	a := []Field{{Name: "status", Type: "int"}, {Name: "time", Type: "timestamp", Format: "clf"}}
	b := []Field{{Name: "status", Type: "int"}, {Name: "time", Type: "timestamp", Format: "clf"}}
	if !fieldsEqual(a, b) {
		t.Error("expected identical fields to be equal")
	}
}

func TestFieldsEqual_DifferentTypeDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "status", Type: "int"}}
	b := []Field{{Name: "status", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields with different Type to not be equal")
	}
}

func TestFieldsEqual_DifferentOrderDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "a", Type: "string"}, {Name: "b", Type: "string"}}
	b := []Field{{Name: "b", Type: "string"}, {Name: "a", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields in a different order to not be equal")
	}
}

func TestFieldsEqual_ExtraNormalizeEntryDoesNotMatch(t *testing.T) {
	a := []Field{{Name: "level", Type: "string", Normalize: []NormalizeRule{{Pattern: "(?i)^warn$", Value: "WARN"}}}}
	b := []Field{{Name: "level", Type: "string"}}
	if fieldsEqual(a, b) {
		t.Error("expected fields with different Normalize entries to not be equal")
	}
}

func TestEncodeFieldsNode_ShorthandForPlainTypeField(t *testing.T) {
	fields := []Field{{Name: "status", Type: "int"}}
	node := encodeFieldsNode(fields)
	if len(node.Content) != 2 {
		t.Fatalf("len(node.Content) = %d, want 2 (one key/value pair)", len(node.Content))
	}
	valueNode := node.Content[1]
	if valueNode.Kind != yaml.ScalarNode {
		t.Errorf("value node Kind = %v, want ScalarNode (shorthand)", valueNode.Kind)
	}
	if valueNode.Value != "int" {
		t.Errorf("value node Value = %q, want %q", valueNode.Value, "int")
	}
}

func TestEncodeFieldsNode_FullMappingForFieldWithFormat(t *testing.T) {
	fields := []Field{{Name: "time", Type: "timestamp", Format: "clf"}}
	node := encodeFieldsNode(fields)
	valueNode := node.Content[1]
	if valueNode.Kind != yaml.MappingNode {
		t.Fatalf("value node Kind = %v, want MappingNode (full form)", valueNode.Kind)
	}
	if idx := findKeyIndex(valueNode, "format"); idx < 0 || valueNode.Content[idx+1].Value != "clf" {
		t.Errorf("expected format: clf in encoded field, got node with content %+v", valueNode.Content)
	}
}

func TestEncodeFieldsNode_RoundTripsKeyExtraReplaceNormalize(t *testing.T) {
	original := []Field{
		{
			Name: "extra",
			Type: "string",
			Key:  "level",
			Replace: []ReplaceRule{
				{Pattern: `\s+`, Replacement: " "},
			},
			Normalize: []NormalizeRule{
				{Pattern: "(?i)^warn$", Value: "WARN"},
			},
		},
		{Name: "raw", Type: "string", Extra: true},
	}

	node := encodeFieldsNode(original)
	out, err := marshalDoc(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}})
	if err != nil {
		t.Fatalf("marshalDoc returned error: %v", err)
	}

	var rule Rule
	ruleSrc := "name: r\nfields:\n"
	for _, line := range bytesSplitLinesIndent(out) {
		ruleSrc += "  " + line + "\n"
	}
	if err := yaml.Unmarshal([]byte(ruleSrc), &rule); err != nil {
		t.Fatalf("yaml.Unmarshal(ruleSrc): %v\n---\n%s", err, ruleSrc)
	}

	if !fieldsEqual(rule.Fields, original) {
		t.Errorf("round-tripped fields = %+v, want %+v", rule.Fields, original)
	}
}

// bytesSplitLinesIndent splits s (already a valid, newline-terminated YAML
// mapping's byte output) into its non-empty lines, for re-indenting it
// under a synthetic wrapper key in a test.
func bytesSplitLinesIndent(s []byte) []string {
	var lines []string
	lines = append(lines, strings.Split(strings.TrimRight(string(s), "\n"), "\n")...)
	return lines
}

func TestExpand_AllPresetsExpandCorrectly(t *testing.T) {
	for _, name := range sortedPresetNames() {
		t.Run(name, func(t *testing.T) {
			input := []byte("rules:\n  - name: r\n    preset: " + name + "\n")
			out, count, err := Expand(input)
			if err != nil {
				t.Fatalf("Expand returned error: %v", err)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}

			cfg, err := loadConfig(out)
			if err != nil {
				t.Fatalf("loadConfig(expanded) returned error: %v\n---\n%s", err, out)
			}
			rule := cfg.Rules[0]
			if rule.Preset != "" {
				t.Errorf("expanded rule still has Preset = %q, want empty", rule.Preset)
			}
			want := presetRegistry[name]
			if rule.Pattern != want.Pattern {
				t.Errorf("Pattern = %q, want %q", rule.Pattern, want.Pattern)
			}
			if !fieldsEqual(rule.Fields, want.Fields) {
				t.Errorf("Fields = %+v, want %+v", rule.Fields, want.Fields)
			}
			if strings.Contains(string(out), "preset:") {
				t.Errorf("expanded output still contains \"preset:\":\n%s", out)
			}
		})
	}
}

func TestExpand_UnknownPresetIsError(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: no_such_preset\n")
	_, _, err := Expand(input)
	if err == nil {
		t.Fatal("expected error for unknown preset")
	}
	want := `rule "access_log": unknown preset "no_such_preset"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestExpand_NonPresetRulesAndCommentsUnchanged(t *testing.T) {
	input := []byte(`# top comment
rules:
  - name: app_log # inline comment
    pattern: '^\[(?P<level>\w+)\] (?P<message>.*)$'
    fields:
      level: string
      message: string
    continuation: '^  (?P<message>.*)$'
`)
	out, count, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	for _, want := range []string{"# top comment", "# inline comment", "continuation:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("expanded output missing %q:\n%s", want, out)
		}
	}
}

func TestExpand_PresetHeadCommentMovesToPatternKey(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    # which format this is\n    preset: apache_clf\n")
	out, _, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if !strings.Contains(string(out), "# which format this is\n    pattern:") {
		t.Errorf("expected head comment to move to the pattern key, got:\n%s", out)
	}
}

func TestExpand_EmptyInputIsNoop(t *testing.T) {
	out, count, err := Expand(nil)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if len(out) != 0 {
		t.Errorf("out = %q, want empty", out)
	}
}

func TestCollapse_ExactMatchCollapsesToPreset(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "preset: apache_clf") {
		t.Errorf("collapsed output missing \"preset: apache_clf\":\n%s", out)
	}
	if strings.Contains(string(out), "pattern:") || strings.Contains(string(out), "fields:") {
		t.Errorf("collapsed output should not contain pattern:/fields::\n%s", out)
	}

	cfg, err := loadConfig(out)
	if err != nil {
		t.Fatalf("loadConfig(collapsed) returned error: %v", err)
	}
	if cfg.Rules[0].Preset != "apache_clf" {
		t.Errorf("Rules[0].Preset = %q, want %q", cfg.Rules[0].Preset, "apache_clf")
	}
}

func TestCollapse_TrivialEscapeDifferenceStillCollapses(t *testing.T) {
	const tempPresetName = "test_escape_variance"
	presetRegistry[tempPresetName] = presetDefinition{
		Pattern: `^(?P<msg>\/test)$`,
		Fields:  []Field{{Name: "msg", Type: "string"}},
	}
	t.Cleanup(func() { delete(presetRegistry, tempPresetName) })

	input := []byte("rules:\n  - name: r\n    pattern: '^(?P<msg>/test)$'\n    fields:\n      msg: string\n")
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "preset: "+tempPresetName) {
		t.Errorf("collapsed output missing preset %q:\n%s", tempPresetName, out)
	}
}

func TestCollapse_SingleFieldDifferenceDoesNotCollapse(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: string
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if strings.Contains(string(out), "preset:") {
		t.Errorf("output should not have collapsed:\n%s", out)
	}
}

func TestCollapse_AlreadyPresetRuleIsNoop(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: apache_clf\n")
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if string(out) != string(input) {
		t.Errorf("output changed for an already-preset rule:\nwant:\n%s\ngot:\n%s", input, out)
	}
}

func TestCollapse_AliasedAndMergedRulesDoNotPanic(t *testing.T) {
	const tempPresetName = "test_alias_variance"
	presetRegistry[tempPresetName] = presetDefinition{
		Pattern: `^(?P<msg>.*)$`,
		Fields:  []Field{{Name: "msg", Type: "string"}},
	}
	t.Cleanup(func() { delete(presetRegistry, tempPresetName) })

	// Row 0 is a literal, directly-declared rule (anchored, but that
	// doesn't change its own node's Kind) and should collapse normally.
	// Row 1 (`- *base`) is a whole-rule alias: its raw node Kind is
	// AliasNode, not MappingNode. Row 2 (`<<: *base`) is a merge key: its
	// raw node has no literal `pattern:`/`fields:` keys of its own. Both
	// must be skipped, not crash, even though cfg.Rules (alias/merge
	// resolved by the yaml decoder) reports all three as pattern/fields
	// matching the temp preset.
	input := []byte(`rules:
  - &base
    name: a
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
  - *base
  - <<: *base
    name: c
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (only the literal anchor row collapses)", count)
	}
	if _, err := loadConfig(out); err != nil {
		t.Fatalf("loadConfig(collapsed) returned error: %v\n---\n%s", err, out)
	}
}

func TestCollapse_WholeRulesSequenceAliasDoesNotPanic(t *testing.T) {
	input := []byte(`_defs: &rules_anchor
  - name: a
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string
rules: *rules_anchor
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if string(out) != string(input) {
		t.Errorf("output changed when rules: itself is an alias:\nwant:\n%s\ngot:\n%s", input, out)
	}
}

func TestCollapse_MetaFieldBlocksCollapse(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr:
        type: string
        meta: source_file
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0 (meta: on remote_addr must block the collapse)", count)
	}
	if strings.Contains(string(out), "preset:") {
		t.Errorf("output should not have collapsed away the meta: attribute:\n%s", out)
	}
}

// lineContaining returns the first line of s that contains substr, or ""
// if no line does - used to check that a comment ended up on the same
// physical line as a particular key, not merely present somewhere in s.
func lineContaining(s, substr string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

func TestExpand_PresetLineCommentMovesToPatternValueLine(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: apache_clf # which format this is\n")
	out, _, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	line := lineContaining(string(out), "pattern:")
	if line == "" {
		t.Fatalf("expanded output has no pattern: line:\n%s", out)
	}
	if !strings.Contains(line, "# which format this is") {
		t.Errorf("expected the preset: line comment to move to the pattern: line, got line:\n%s\nfull output:\n%s", line, out)
	}
}

func TestCollapse_PatternLineCommentMovesToPresetValueLine(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$' # which format this is
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	line := lineContaining(string(out), "preset:")
	if line == "" {
		t.Fatalf("collapsed output has no preset: line:\n%s", out)
	}
	if !strings.Contains(line, "# which format this is") {
		t.Errorf("expected the pattern: line comment to move to the preset: line, got line:\n%s\nfull output:\n%s", line, out)
	}
}

func TestExpand_PresetFootCommentSurvives(t *testing.T) {
	input := []byte("rules:\n  - name: access_log\n    preset: apache_clf\n    # trailing note about this rule\n  - name: other\n    preset: apache_clf\n")
	out, count, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	got := string(out)
	commentIdx := strings.Index(got, "# trailing note about this rule")
	if commentIdx < 0 {
		t.Fatalf("expected the preset: foot comment to survive expansion, got:\n%s", got)
	}
	// The comment belongs to the first rule, not the second: it must land
	// on or before the fields: block that pattern:/fields: expanded into,
	// strictly before the second rule's "name: other" starts, and must not
	// introduce a spurious blank line splitting the first rule's own
	// pattern:/fields: block in two.
	fieldsIdx := strings.Index(got, "fields:")
	secondRuleIdx := strings.Index(got, "name: other")
	if fieldsIdx < 0 || fieldsIdx > commentIdx {
		t.Errorf("expected the foot comment to land within/after the first rule's fields: block, not before it, got:\n%s", got)
	}
	if secondRuleIdx < 0 || commentIdx > secondRuleIdx {
		t.Errorf("expected the foot comment to stay attached to the first rule, before the second rule starts, got:\n%s", got)
	}
	if strings.Contains(got, "\n\n") {
		t.Errorf("expected no spurious blank line from moving the foot comment, got:\n%s", got)
	}
}

func TestCollapse_StandaloneCommentBetweenPatternAndFieldsSurvives(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    # which format this is
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "# which format this is") {
		t.Errorf("expected the standalone comment between pattern: and fields: to survive collapse, got:\n%s", out)
	}
}

func TestCollapse_StandaloneCommentAfterFieldsSurvives(t *testing.T) {
	input := []byte(`rules:
  - name: access_log
    pattern: '^(?P<remote_addr>\S+) - (?P<remote_user>\S+) \[(?P<time>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) (?P<proto>\S+)" (?P<status>\d+) (?P<bytes>\d+)$'
    fields:
      remote_addr: string
      remote_user: string
      time:
        type: timestamp
        format: clf
      method: string
      path: string
      proto: string
      status: int
      bytes: int
    # trailing note about this rule
  - name: other
    pattern: '(?P<msg>.*)'
    fields:
      msg: string
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if !strings.Contains(string(out), "# trailing note about this rule") {
		t.Errorf("expected the standalone comment trailing fields: to survive collapse, got:\n%s", out)
	}
}

func TestCollapse_ZeroMatchesPreservesInputByteForByte(t *testing.T) {
	input := []byte(`rules:
  - name: r1
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string

  - name: r2
    pattern: '^(?P<other>.*)$'
    fields:
      other: string
`)
	out, count, err := Collapse(input)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if string(out) != string(input) {
		t.Errorf("output changed for a Collapse that converted nothing:\nwant:\n%s\ngot:\n%s", input, out)
	}
}

func TestExpand_ZeroConversionsPreservesBlankLines(t *testing.T) {
	input := []byte(`rules:
  - name: r1
    pattern: '^(?P<msg>.*)$'
    fields:
      msg: string

  - name: r2
    pattern: '^(?P<other>.*)$'
    fields:
      other: string
`)
	out, count, err := Expand(input)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if string(out) != string(input) {
		t.Errorf("output changed for an Expand that converted nothing:\nwant:\n%s\ngot:\n%s", input, out)
	}
}

func TestExpand_PreExistingValidationErrorSurfacesDirectly(t *testing.T) {
	input := []byte("rules:\n  - name: r\n    pattern: '^(?P<a>.*)$'\n    fields:\n      b: string\n")
	_, _, err := Expand(input)
	if err == nil {
		t.Fatal("expected error for a field with no matching capture group")
	}
	if strings.Contains(err.Error(), "this is a bug") {
		t.Errorf("a pre-existing input error should not be reported as \"this is a bug\": %v", err)
	}
	want := `rule "r": field "b" has no matching named capture group in pattern`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestExpandThenCollapse_RoundTripsBackToPreset(t *testing.T) {
	original := []byte("rules:\n  - name: access_log\n    preset: apache_clf\n")

	expanded, count, err := Expand(original)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("Expand count = %d, want 1", count)
	}

	collapsed, count, err := Collapse(expanded)
	if err != nil {
		t.Fatalf("Collapse returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("Collapse count = %d, want 1", count)
	}

	cfg, err := loadConfig(collapsed)
	if err != nil {
		t.Fatalf("loadConfig(collapsed) returned error: %v", err)
	}
	if cfg.Rules[0].Preset != "apache_clf" {
		t.Errorf("round-tripped Preset = %q, want %q", cfg.Rules[0].Preset, "apache_clf")
	}
}
