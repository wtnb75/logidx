package rules

import (
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
