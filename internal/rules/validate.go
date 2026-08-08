package rules

import (
	"errors"
	"fmt"
)

var allowedTypes = map[string]bool{
	"string":    true,
	"int":       true,
	"float":     true,
	"timestamp": true,
}

// Validate checks all fail-fast startup invariants described in the design
// spec and returns a joined error listing every violation found.
func (c *Config) Validate() error {
	var errs []error
	firstFieldsByName := map[string][]Field{}

	for _, rule := range c.Rules {
		captureNames := map[string]bool{}
		for _, n := range rule.Regexp.SubexpNames() {
			if n != "" {
				captureNames[n] = true
			}
		}

		for _, field := range rule.Fields {
			if !captureNames[field.Name] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has no matching named capture group in pattern", rule.Name, field.Name))
			}
			if !allowedTypes[field.Type] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported type %q", rule.Name, field.Name, field.Type))
			}
			if field.Type == "timestamp" && field.Format == "" {
				errs = append(errs, fmt.Errorf("rule %q: field %q is type timestamp but has no format", rule.Name, field.Name))
			}
		}

		if rule.ContinuationRegexp != nil {
			fieldNames := map[string]bool{}
			for _, field := range rule.Fields {
				fieldNames[field.Name] = true
			}
			for _, n := range rule.ContinuationRegexp.SubexpNames() {
				if n != "" && !fieldNames[n] {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q with no matching declared field", rule.Name, n))
				}
			}
		}

		if existing, ok := firstFieldsByName[rule.Name]; ok {
			if !fieldsEqualForSchema(existing, rule.Fields) {
				errs = append(errs, fmt.Errorf("rule %q: multiple rules share this name but declare different fields (name+type, in the same order, must match exactly)", rule.Name))
			}
		} else {
			firstFieldsByName[rule.Name] = rule.Fields
		}
	}

	if err := c.Compression.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("compression: %w", err))
	}
	if err := c.RowGroup.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("row_group: %w", err))
	}

	return errors.Join(errs...)
}

// fieldsEqualForSchema compares two field sequences by name+type, in order
// (order now determines output column order, so two same-name rules
// declaring identical fields in a different order are still a conflict),
// ignoring Format and Normalize per the design's schema-consistency rule.
func fieldsEqualForSchema(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Type != b[i].Type {
			return false
		}
	}
	return true
}
