package convert

import "logidx/internal/rules"

// mergeKeyField returns, for each distinct rule name in ruleList, the name
// of its first Type == "timestamp" field in declaration order — the field
// internal/convert.mergeFiles uses to globally order that rule's matched
// rows across every input file. Rules with no timestamp field are omitted
// from the result; their matched rows are written in plain file-arrival
// order instead (see fileCursor.advance).
func mergeKeyField(ruleList []rules.Rule) map[string]string {
	result := map[string]string{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		for _, field := range r.Fields {
			if field.Type == "timestamp" {
				result[r.Name] = field.Name
				break
			}
		}
	}
	return result
}
