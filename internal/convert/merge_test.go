package convert

import (
	"testing"

	"logidx/internal/rules"
)

func TestMergeKeyField_PicksFirstTimestampFieldInDeclarationOrder(t *testing.T) {
	ruleList := []rules.Rule{
		{
			Name: "with_two_timestamps",
			Fields: []rules.Field{
				{Name: "level", Type: "string"},
				{Name: "start", Type: "timestamp"},
				{Name: "end", Type: "timestamp"},
			},
		},
		{
			Name: "no_timestamp",
			Fields: []rules.Field{
				{Name: "level", Type: "string"},
			},
		},
	}

	got := mergeKeyField(ruleList)

	if got["with_two_timestamps"] != "start" {
		t.Errorf("with_two_timestamps merge key = %q, want %q", got["with_two_timestamps"], "start")
	}
	if _, ok := got["no_timestamp"]; ok {
		t.Errorf("no_timestamp should have no merge key, got %q", got["no_timestamp"])
	}
}

func TestMergeKeyField_SameNameRulesUseFirstOccurrence(t *testing.T) {
	// rules.Validate guarantees same-name rules declare identical
	// name+type fields, so taking the first occurrence (like
	// schema.BuildAll does) is always consistent with the rest.
	ruleList := []rules.Rule{
		{
			Name:   "dup",
			Fields: []rules.Field{{Name: "ts", Type: "timestamp"}},
		},
		{
			Name:   "dup",
			Fields: []rules.Field{{Name: "ts", Type: "timestamp"}},
		},
	}

	got := mergeKeyField(ruleList)

	if len(got) != 1 || got["dup"] != "ts" {
		t.Errorf("mergeKeyField() = %v, want map[dup:ts]", got)
	}
}
