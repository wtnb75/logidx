package rules

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// NormalizeRule maps a captured raw string to a canonical value when Pattern matches.
type NormalizeRule struct {
	Pattern string         `yaml:"pattern"`
	Value   string         `yaml:"value"`
	Regexp  *regexp.Regexp `yaml:"-"`
}

// Field describes how a named capture group should be typed and normalized.
type Field struct {
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
// capture groups.
type Rule struct {
	Name    string          `yaml:"name"`
	Pattern string          `yaml:"pattern"`
	Fields  map[string]Field `yaml:"fields"`
	Regexp  *regexp.Regexp  `yaml:"-"`
}

// Config is the top-level rules.yaml document.
type Config struct {
	Rules []Rule `yaml:"rules"`
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

		for name, field := range cfg.Rules[i].Fields {
			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, name, err)
				}
				field.Normalize[j].Regexp = nre
			}
			cfg.Rules[i].Fields[name] = field
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
