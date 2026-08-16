package parse

import (
	"testing"

	"github.com/wtnb75/logidx/internal/rules"
)

func mustMaskRuleT(t *testing.T, typ, pattern, action, value string, length int) rules.MaskRule {
	t.Helper()
	re := mustCompileT(t, pattern)
	return rules.MaskRule{Type: typ, Pattern: pattern, Regexp: re, Action: action, Value: value, Length: length}
}

func TestSplitMaskRules_PartitionsByTypePreservingOrder(t *testing.T) {
	mask := []rules.MaskRule{
		mustMaskRuleT(t, "key", "a", "redact", "[A]", 0),
		mustMaskRuleT(t, "pattern", "b", "redact", "[B]", 0),
		mustMaskRuleT(t, "key", "c", "redact", "[C]", 0),
	}

	keyRules, patternRules := SplitMaskRules(mask)

	if len(keyRules) != 2 || keyRules[0].Pattern != "a" || keyRules[1].Pattern != "c" {
		t.Errorf("keyRules = %+v, want patterns [a c]", keyRules)
	}
	if len(patternRules) != 1 || patternRules[0].Pattern != "b" {
		t.Errorf("patternRules = %+v, want patterns [b]", patternRules)
	}
}

func TestApplyKeyMaskJSON_TopLevelKeyMatch(t *testing.T) {
	tree := map[string]any{"password": "hunter2", "user": "alice"}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	if tree["password"] != "[MASKED]" {
		t.Errorf("password = %v, want [MASKED]", tree["password"])
	}
	if tree["user"] != "alice" {
		t.Errorf("user = %v, want unchanged alice", tree["user"])
	}
}

func TestApplyKeyMaskJSON_NestedObjectKeyMatch(t *testing.T) {
	tree := map[string]any{
		"user": map[string]any{"email": "a@example.com", "name": "alice"},
	}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^email$", "redact", "[EMAIL]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	nested := tree["user"].(map[string]any)
	if nested["email"] != "[EMAIL]" {
		t.Errorf("nested email = %v, want [EMAIL]", nested["email"])
	}
	if nested["name"] != "alice" {
		t.Errorf("nested name = %v, want unchanged alice", nested["name"])
	}
}

func TestApplyKeyMaskJSON_ArrayOfObjectsKeyMatch(t *testing.T) {
	tree := map[string]any{
		"users": []any{
			map[string]any{"password": "p1"},
			map[string]any{"password": "p2"},
		},
	}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskJSON(tree, keyRules)

	users := tree["users"].([]any)
	for i, u := range users {
		if u.(map[string]any)["password"] != "[MASKED]" {
			t.Errorf("users[%d].password = %v, want [MASKED]", i, u.(map[string]any)["password"])
		}
	}
}

func TestApplyKeyMaskJSON_MultipleRulesChainOnSameKey(t *testing.T) {
	tree := map[string]any{"token": "secret"}
	keyRules := []rules.MaskRule{
		mustMaskRuleT(t, "key", "(?i)^token$", "hash", "", 8),
		mustMaskRuleT(t, "key", "(?i)^token$", "redact", "[CHAINED]", 0),
	}

	applyKeyMaskJSON(tree, keyRules)

	if tree["token"] != "[CHAINED]" {
		t.Errorf("token = %v, want [CHAINED] (second rule's redact must apply after first rule's hash)", tree["token"])
	}
}

func TestApplyKeyMaskFlat_TopLevelKeyMatch(t *testing.T) {
	m := map[string]string{"password": "hunter2", "user": "alice"}
	keyRules := []rules.MaskRule{mustMaskRuleT(t, "key", "(?i)^password$", "redact", "[MASKED]", 0)}

	applyKeyMaskFlat(m, keyRules)

	if m["password"] != "[MASKED]" {
		t.Errorf("password = %q, want [MASKED]", m["password"])
	}
	if m["user"] != "alice" {
		t.Errorf("user = %q, want unchanged alice", m["user"])
	}
}

func TestApplyPatternMask_Redact(t *testing.T) {
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `[\w.+-]+@[\w.-]+\.\w+`, "redact", "[EMAIL]", 0)}

	got := ApplyPatternMask("contact admin@example.com now", patternRules)

	want := "contact [EMAIL] now"
	if got != want {
		t.Errorf("ApplyPatternMask() = %q, want %q", got, want)
	}
}

func TestApplyPatternMask_HashReplacesEachMatchIndependently(t *testing.T) {
	patternRules := []rules.MaskRule{mustMaskRuleT(t, "pattern", `\d+`, "hash", "", 8)}

	got := ApplyPatternMask("id=111 other=222", patternRules)

	h111 := hashTrunc("111", 8)
	h222 := hashTrunc("222", 8)
	want := "id=" + h111 + " other=" + h222
	if got != want {
		t.Errorf("ApplyPatternMask() = %q, want %q", got, want)
	}
}

func TestApplyPatternMask_MultipleRulesChain(t *testing.T) {
	patternRules := []rules.MaskRule{
		mustMaskRuleT(t, "pattern", `foo`, "redact", "bar", 0),
		mustMaskRuleT(t, "pattern", `bar`, "redact", "baz", 0),
	}

	got := ApplyPatternMask("foo", patternRules)

	if got != "baz" {
		t.Errorf("ApplyPatternMask() = %q, want %q (second rule must see first rule's output)", got, "baz")
	}
}

func TestHashTrunc_Deterministic(t *testing.T) {
	a := hashTrunc("same-input", 16)
	b := hashTrunc("same-input", 16)
	if a != b {
		t.Errorf("hashTrunc not deterministic: %q != %q", a, b)
	}
}

func TestHashTrunc_LengthMatchesRequest(t *testing.T) {
	for _, length := range []int{1, 8, 32, 64} {
		got := hashTrunc("x", length)
		if len(got) != length {
			t.Errorf("hashTrunc(%q, %d) length = %d, want %d", "x", length, len(got), length)
		}
	}
}
