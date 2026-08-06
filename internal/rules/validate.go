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
	firstFieldsByName := map[string]map[string]Field{}

	for _, rule := range c.Rules {
		captureNames := map[string]bool{}
		for _, n := range rule.Regexp.SubexpNames() {
			if n != "" {
				captureNames[n] = true
			}
		}

		for fieldName, field := range rule.Fields {
			if !captureNames[fieldName] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has no matching named capture group in pattern", rule.Name, fieldName))
			}
			if !allowedTypes[field.Type] {
				errs = append(errs, fmt.Errorf("rule %q: field %q has unsupported type %q", rule.Name, fieldName, field.Type))
			}
			if field.Type == "timestamp" && field.Format == "" {
				errs = append(errs, fmt.Errorf("rule %q: field %q is type timestamp but has no format", rule.Name, fieldName))
			}
		}

		if existing, ok := firstFieldsByName[rule.Name]; ok {
			if !fieldsEqualForSchema(existing, rule.Fields) {
				errs = append(errs, fmt.Errorf("rule %q: multiple rules share this name but declare different fields (name+type must match exactly)", rule.Name))
			}
		} else {
			firstFieldsByName[rule.Name] = rule.Fields
		}
	}

	if err := c.Compression.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("compression: %w", err))
	}

	return errors.Join(errs...)
}

// fieldsEqualForSchema compares two field sets by name+type only, ignoring
// Format and Normalize, per the design's schema-consistency rule.
func fieldsEqualForSchema(a, b map[string]Field) bool {
	if len(a) != len(b) {
		return false
	}
	for name, fa := range a {
		fb, ok := b[name]
		if !ok || fa.Type != fb.Type {
			return false
		}
	}
	return true
}
