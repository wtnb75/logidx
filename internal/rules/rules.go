package rules

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"

	"logidx/internal/compression"
)

// NormalizeRule maps a captured raw string to a canonical value when Pattern matches.
type NormalizeRule struct {
	Pattern string         `yaml:"pattern"`
	Value   string         `yaml:"value"`
	Regexp  *regexp.Regexp `yaml:"-"`
}

// Field describes how a named capture group should be typed and normalized.
// Name is set by Rule's custom UnmarshalYAML, the only place in this package
// that knows the field's declaration order in the source YAML - see Rule.
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Normalize []NormalizeRule `yaml:"normalize"`
}

// UnmarshalYAML supports both the shorthand `name: string` form and the
// full mapping form `name: {type: ..., format: ..., normalize: [...]}`.
func (f *Field) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		f.Type = value.Value
		return nil
	}

	type fieldAlias Field
	var alias fieldAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	*f = Field(alias)
	return nil
}

// Rule is a single pattern-match rule: a name (output type), the regexp
// pattern used to match lines, and the fields extracted from named
// capture groups. Fields is ordered the way they were declared in
// rules.yaml, which becomes the output Parquet file's column order (see
// internal/schema.Build) - this is why Rule needs its own UnmarshalYAML:
// decoding `fields:` into a Go map (the obvious approach) would silently
// lose that declaration order, since map iteration order is unspecified.
type Rule struct {
	Name    string         `yaml:"name"`
	Pattern string         `yaml:"pattern"`
	Fields  []Field        `yaml:"-"`
	Regexp  *regexp.Regexp `yaml:"-"`
}

// UnmarshalYAML decodes name and pattern normally, but walks the fields
// mapping node directly (instead of decoding it into a Go map) so field
// declaration order is preserved. The YAML syntax for `fields:` is
// unchanged - still a mapping of name to type/definition - only the
// in-memory representation differs from what plain struct decoding would
// produce.
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var alias struct {
		Name    string    `yaml:"name"`
		Pattern string    `yaml:"pattern"`
		Fields  yaml.Node `yaml:"fields"`
	}
	if err := value.Decode(&alias); err != nil {
		return err
	}
	r.Name = alias.Name
	r.Pattern = alias.Pattern

	if alias.Fields.Kind == 0 {
		return nil // no `fields:` key present
	}
	if alias.Fields.Kind != yaml.MappingNode {
		return fmt.Errorf("rule %q: fields must be a mapping", r.Name)
	}

	r.Fields = make([]Field, 0, len(alias.Fields.Content)/2)
	for i := 0; i+1 < len(alias.Fields.Content); i += 2 {
		nameNode, defNode := alias.Fields.Content[i], alias.Fields.Content[i+1]

		var field Field
		if err := field.UnmarshalYAML(defNode); err != nil {
			return fmt.Errorf("rule %q: field %q: %w", r.Name, nameNode.Value, err)
		}
		field.Name = nameNode.Value
		r.Fields = append(r.Fields, field)
	}
	return nil
}

// Config is the top-level rules.yaml document.
type Config struct {
	Rules []Rule `yaml:"rules"`
	// Compression optionally sets the output Parquet compression codec and
	// level; unset fields fall back to the CLI flags, then to the default
	// (see internal/compression).
	Compression compression.Settings `yaml:"compression"`
}

// Load reads, parses, compiles, and validates a rules YAML file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse rules YAML: %w", err)
	}

	for i := range cfg.Rules {
		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern: %w", cfg.Rules[i].Name, err)
		}
		cfg.Rules[i].Regexp = re

		for fi := range cfg.Rules[i].Fields {
			field := &cfg.Rules[i].Fields[fi]
			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Normalize[j].Regexp = nre
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
