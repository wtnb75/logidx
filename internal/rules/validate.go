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

var allowedStructuredFormats = map[string]bool{
	"json":   true,
	"ltsv":   true,
	"logfmt": true,
}

// Validate checks all fail-fast startup invariants described in the design
// spec and returns a joined error listing every violation found.
func (c *Config) Validate() error {
	var errs []error
	firstFieldsByName := map[string][]Field{}

	for _, rule := range c.Rules {
		if rule.Preset != "" {
			if _, ok := presetRegistry[rule.Preset]; !ok {
				errs = append(errs, fmt.Errorf("rule %q: unknown preset %q", rule.Name, rule.Preset))
			}
			if rule.declaredPatternOrFields {
				errs = append(errs, fmt.Errorf("rule %q: preset and pattern/fields are mutually exclusive", rule.Name))
			}
		}

		captureNames := map[string]bool{}
		for _, n := range rule.Regexp.SubexpNames() {
			if n != "" {
				captureNames[n] = true
			}
		}

		extraCount := 0
		for _, field := range rule.Fields {
			usesStructured := field.Key != "" || field.Extra
			if !usesStructured && !captureNames[field.Name] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has no matching named capture group in pattern", rule.Name, field.Name))
			}
			if !allowedTypes[field.Type] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported type %q", rule.Name, field.Name, field.Type))
			}
			if field.Type == "timestamp" && field.Format == "" {
				errs = append(errs, fmt.Errorf("rule %q: field %q is type timestamp but has no format", rule.Name, field.Name))
			}
			if field.Key != "" && field.Extra {
				errs = append(errs, fmt.Errorf("rule %q: field %q sets both key and extra", rule.Name, field.Name))
			}
			if usesStructured && rule.Structured == nil {
				errs = append(errs, fmt.Errorf("rule %q: field %q uses key/extra but the rule has no structured config", rule.Name, field.Name))
			}
			if field.Extra {
				extraCount++
			}
		}
		if extraCount > 1 {
			errs = append(errs, fmt.Errorf("rule %q: more than one field has extra: true (max 1 per rule)", rule.Name))
		}

		if rule.Structured != nil {
			if !allowedStructuredFormats[rule.Structured.Format] {
				errs = append(errs, fmt.Errorf("rule %q: structured format %q is not one of json/ltsv/logfmt", rule.Name, rule.Structured.Format))
			}
			if !captureNames[rule.Structured.Source] {
				errs = append(errs, fmt.Errorf("rule %q: structured source %q has no matching named capture group in pattern", rule.Name, rule.Structured.Source))
			}
		}

		if rule.ContinuationRegexp != nil {
			fieldsByName := map[string]Field{}
			for _, field := range rule.Fields {
				fieldsByName[field.Name] = field
			}
			for _, n := range rule.ContinuationRegexp.SubexpNames() {
				if n == "" {
					continue
				}
				field, ok := fieldsByName[n]
				if !ok {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q with no matching declared field", rule.Name, n))
					continue
				}
				if field.Key != "" || field.Extra {
					errs = append(errs, fmt.Errorf("rule %q: continuation pattern has named capture group %q targets field %q, which takes its value from structured data (key/extra) instead of the pattern", rule.Name, n, n))
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
