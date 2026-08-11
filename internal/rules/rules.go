package rules

import (
	"fmt"
	"os"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"

	"logidx/internal/compression"
	"logidx/internal/rowgroup"
)

// FieldMetaSourceFile and FieldMetaSourceLine are the only two values
// Field.Meta accepts. See parse.SourceMeta for how they're resolved.
const (
	FieldMetaSourceFile = "source_file"
	FieldMetaSourceLine = "source_line"
)

// NormalizeRule maps a captured raw string to a canonical value when Pattern matches.
type NormalizeRule struct {
	Pattern string         `yaml:"pattern"`
	Value   string         `yaml:"value"`
	Regexp  *regexp.Regexp `yaml:"-"`
}

// ReplaceRule replaces every regexp match of Pattern within a field's raw
// value with Replacement (Go's regexp.ReplaceAllString - $1-style capture
// group backreferences work without any extra code). Declared rules chain:
// each rule's output becomes the next rule's input.
type ReplaceRule struct {
	Pattern     string         `yaml:"pattern"`
	Replacement string         `yaml:"value"`
	Regexp      *regexp.Regexp `yaml:"-"`
}

// StructuredConfig configures parsing embedded structured data (JSON, LTSV,
// logfmt, or a preset's fixed pattern) out of one of a rule's
// pattern-captured fields, named by Source. Format selects the parser:
// "json", "ltsv", "logfmt", or the name of an entry in presetRegistry (see
// presets.go).
type StructuredConfig struct {
	Source string `yaml:"source"`
	Format string `yaml:"format"`

	// PresetRegexp is set by Load, once, when Format names a registered
	// preset instead of json/ltsv/logfmt: it's presetRegistry[Format]'s
	// Pattern, compiled (same "compile once at Load time" approach as
	// Rule.Regexp). nil when Format is json/ltsv/logfmt. parse.Convert
	// branches on this to pick ParsePreset over ParseStructured.
	PresetRegexp *regexp.Regexp `yaml:"-"`
}

// Field describes how a named capture group should be typed and normalized.
// Name is set by Rule's custom UnmarshalYAML, the only place in this package
// that knows the field's declaration order in the source YAML - see Rule.
type Field struct {
	Name      string          `yaml:"-"`
	Type      string          `yaml:"type"`
	Format    string          `yaml:"format"`
	Replace   []ReplaceRule   `yaml:"replace"`
	Normalize []NormalizeRule `yaml:"normalize"`

	// Key, if set, takes this field's raw value from the rule's parsed
	// structured data (see Rule.Structured) under this key name, instead
	// of from a same-named pattern capture group.
	Key string `yaml:"key"`
	// Extra, if true, collects every structured-data key not consumed by
	// another field's Key into this field as a JSON string. At most one
	// field per rule may set Extra.
	Extra bool `yaml:"extra"`
	// Meta, if set to FieldMetaSourceFile or FieldMetaSourceLine, takes
	// this field's raw value from the current input line's source
	// metadata (see parse.SourceMeta) instead of a pattern capture group
	// or structured data. Unlike Key/Extra, a Meta field never reads
	// structured data, so it does not require the rule to declare
	// Structured. Empty for every field that isn't opted in.
	Meta string `yaml:"meta"`

	// ResolvedFormat is Format resolved once by ResolveFormat, at Load
	// time - see TimeFormat. Only meaningful when Type == "timestamp".
	ResolvedFormat TimeFormat `yaml:"-"`
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

	// Preset, if set, names a fixed pattern/fields definition from
	// presetRegistry (see presets.go) that Load expands into Pattern/
	// Fields before compiling. Mutually exclusive with declaring pattern/
	// fields directly - Validate checks this using
	// declaredPatternOrFields, captured here at YAML-decode time, before
	// Load's expansion overwrites Pattern/Fields with the preset's
	// values.
	Preset                  string `yaml:"preset"`
	declaredPatternOrFields bool   `yaml:"-"`

	// Structured optionally parses one of this rule's captured fields
	// (Structured.Source) as JSON/LTSV/logfmt, letting other fields pull
	// values out of it by key (see Field.Key/Field.Extra) instead of by
	// capture group position. Populated by Rule's UnmarshalYAML, the only
	// place that reads the `structured:` key.
	Structured *StructuredConfig `yaml:"-"`

	// Continuation is an optional regexp pattern. A line matching it while
	// this rule has an in-progress multi-line entry open (see
	// internal/convert.fileCursor) is folded into that entry instead of
	// starting a new one; the pattern's named capture groups say which
	// field(s) receive the matched content. Unset means this rule is
	// always single-line, matching pre-existing behavior.
	Continuation       string         `yaml:"continuation"`
	ContinuationRegexp *regexp.Regexp `yaml:"-"`
}

// UnmarshalYAML decodes name and pattern normally, but walks the fields
// mapping node directly (instead of decoding it into a Go map) so field
// declaration order is preserved. The YAML syntax for `fields:` is
// unchanged - still a mapping of name to type/definition - only the
// in-memory representation differs from what plain struct decoding would
// produce.
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var alias struct {
		Name         string            `yaml:"name"`
		Pattern      string            `yaml:"pattern"`
		Preset       string            `yaml:"preset"`
		Continuation string            `yaml:"continuation"`
		Structured   *StructuredConfig `yaml:"structured"`
		Fields       yaml.Node         `yaml:"fields"`
	}
	if err := value.Decode(&alias); err != nil {
		return err
	}
	r.Name = alias.Name
	r.Pattern = alias.Pattern
	r.Preset = alias.Preset
	r.Continuation = alias.Continuation
	r.Structured = alias.Structured
	r.declaredPatternOrFields = alias.Pattern != "" || alias.Fields.Kind != 0

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
	// RowGroup optionally caps the number of rows per Parquet row group on
	// every output file; unset falls back to the CLI flag, then to
	// unlimited (see internal/rowgroup).
	RowGroup rowgroup.Settings `yaml:"row_group"`
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
		if cfg.Rules[i].Preset != "" {
			if preset, ok := presetRegistry[cfg.Rules[i].Preset]; ok {
				cfg.Rules[i].Pattern = preset.Pattern
				cfg.Rules[i].Fields = slices.Clone(preset.Fields)
			}
			// An unknown preset name is left un-expanded (Pattern/Fields
			// stay whatever the user wrote, possibly empty) - Validate
			// reports "unknown preset" and Load returns that error, so
			// this rule's half-expanded state never reaches a caller.
		}

		if cfg.Rules[i].Structured != nil {
			format := cfg.Rules[i].Structured.Format
			if !builtinStructuredFormats[format] {
				if preset, ok := presetRegistry[format]; ok {
					pre, err := regexp.Compile(preset.Pattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q: compile structured preset pattern %q: %w", cfg.Rules[i].Name, format, err)
					}
					cfg.Rules[i].Structured.PresetRegexp = pre
				}
				// An unknown format (neither builtin nor a registered
				// preset) is left unresolved - Validate reports "not
				// json/ltsv/logfmt or a known preset name" and Load
				// returns that error, so this rule's nil PresetRegexp
				// never reaches a caller.
			}
		}

		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern: %w", cfg.Rules[i].Name, err)
		}
		cfg.Rules[i].Regexp = re

		if cfg.Rules[i].Continuation != "" {
			cre, err := regexp.Compile(cfg.Rules[i].Continuation)
			if err != nil {
				return nil, fmt.Errorf("rule %q: compile continuation pattern: %w", cfg.Rules[i].Name, err)
			}
			cfg.Rules[i].ContinuationRegexp = cre
		}

		for fi := range cfg.Rules[i].Fields {
			field := &cfg.Rules[i].Fields[fi]
			for j := range field.Replace {
				rre, err := regexp.Compile(field.Replace[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile replace pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Replace[j].Regexp = rre
			}

			for j := range field.Normalize {
				nre, err := regexp.Compile(field.Normalize[j].Pattern)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: compile normalize pattern: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.Normalize[j].Regexp = nre
			}

			if field.Type == "timestamp" {
				tf, err := ResolveFormat(field.Format)
				if err != nil {
					return nil, fmt.Errorf("rule %q field %q: %w", cfg.Rules[i].Name, field.Name, err)
				}
				field.ResolvedFormat = tf
			}
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
