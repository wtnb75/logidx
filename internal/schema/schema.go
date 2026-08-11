package schema

import (
	"fmt"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"

	"github.com/wtnb75/logidx/internal/rules"
)

// Built holds a derived Parquet schema together with the field-declaration
// order used to build it, so callers can construct rows in matching order.
type Built struct {
	Schema  *parquet.Schema
	Columns []string
}

// orderedGroup is a parquet.Node that behaves exactly like the
// parquet.Group it wraps, except its Fields() returns fields in order
// instead of parquet.Group's always-alphabetical order (parquet.Group is a
// plain map, so Fields() sorts by name for determinism - see its
// implementation). Every other Node method is inherited unchanged via
// embedding; only Fields() needs overriding because it's the method every
// schema-walking code path (writer column layout, footer serialization,
// readers) uses to enumerate a group's children.
type orderedGroup struct {
	parquet.Group
	order []string
}

func (g orderedGroup) Fields() []parquet.Field {
	byName := make(map[string]parquet.Field, len(g.order))
	for _, f := range g.Group.Fields() {
		byName[f.Name()] = f
	}
	ordered := make([]parquet.Field, len(g.order))
	for i, name := range g.order {
		ordered[i] = byName[name]
	}
	return ordered
}

// NewOrderedGroup builds a group Node from fields whose Fields() returns
// them in the given order, rather than parquet.Group's always-alphabetical
// order. order must list exactly fields' keys (each exactly once). Every
// caller that reconstructs a schema outside of Build (pqcopy recompressing
// an existing file, pqdump restoring one) needs this same fix - a plain
// parquet.Group would silently re-alphabetize columns even when the caller
// already has the right order in hand.
func NewOrderedGroup(fields map[string]parquet.Node, order []string) parquet.Node {
	return orderedGroup{Group: parquet.Group(fields), order: order}
}

// Build derives a Parquet schema for the given rule name from its field
// definitions, in the order they were declared (see rules.Rule.Fields).
func Build(name string, fields []rules.Field) (*Built, error) {
	group := map[string]parquet.Node{}
	names := make([]string, len(fields))
	for i, field := range fields {
		node, err := NodeForType(field.Type)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", field.Name, err)
		}
		group[field.Name] = parquet.Required(node)
		names[i] = field.Name
	}

	return &Built{
		Schema:  parquet.NewSchema(name, NewOrderedGroup(group, names)),
		Columns: names,
	}, nil
}

// BuildAll derives one Built schema per distinct rule name in ruleList,
// using the first rule's field definitions for each name (rules.Validate
// guarantees same-name rules declare identical name+type fields).
func BuildAll(ruleList []rules.Rule) (map[string]*Built, error) {
	result := map[string]*Built{}
	for _, r := range ruleList {
		if _, exists := result[r.Name]; exists {
			continue
		}
		built, err := Build(r.Name, r.Fields)
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		result[r.Name] = built
	}
	return result, nil
}

// knownTypeNames lists every field type name NodeForType accepts, in the
// order TypeName checks them.
var knownTypeNames = []string{"string", "int", "float", "timestamp"}

// NodeForType returns the Parquet leaf node for one of the field type names
// used in rules.yaml ("string", "int", "float", "timestamp").
func NodeForType(t string) (parquet.Node, error) {
	switch t {
	case "string":
		return parquet.String(), nil
	case "int":
		return parquet.Int(64), nil
	case "float":
		return parquet.Leaf(parquet.DoubleType), nil
	case "timestamp":
		return parquet.Timestamp(parquet.Microsecond), nil
	default:
		return nil, fmt.Errorf("unsupported field type %q", t)
	}
}

// TypeName returns the field type name (NodeForType's vocabulary) for a
// node's Parquet type, the inverse of NodeForType. It is used to describe an
// already-built schema (e.g. read back from a Parquet file's footer) in the
// same vocabulary rules.yaml uses. It returns an error if node's type isn't
// one NodeForType produces, e.g. an arbitrary externally-produced Parquet
// file using a type this package doesn't model.
func TypeName(node parquet.Node) (string, error) {
	for _, name := range knownTypeNames {
		want, _ := NodeForType(name) // always succeeds: name is our own constant
		if node.Type().String() == want.Type().String() {
			return name, nil
		}
	}
	return "", fmt.Errorf("unsupported parquet type %s", node.Type())
}

// ForceCompression rebuilds node's schema tree with every leaf column
// wrapped to report codec from Compression(), overriding whatever codec (if
// any) the original leaf reported. It preserves node.Fields()'s order via
// NewOrderedGroup - a plain parquet.Group is a map and would otherwise
// silently re-alphabetize columns, which matters here because callers pass
// in a schema whose Fields() order came from an existing on-disk file (see
// pqcat.Cat, and formerly pqcopy.Copy).
func ForceCompression(node parquet.Node, codec compress.Codec) parquet.Node {
	if node.Leaf() {
		return parquet.Compressed(node, codec)
	}

	fields := node.Fields()
	names := make([]string, len(fields))
	group := make(map[string]parquet.Node, len(fields))
	for i, f := range fields {
		names[i] = f.Name()
		group[f.Name()] = ForceCompression(f, codec)
	}

	out := NewOrderedGroup(group, names)
	switch {
	case node.Optional():
		out = parquet.Optional(out)
	case node.Repeated():
		out = parquet.Repeated(out)
	}
	return out
}

// Equal compares two flat (non-nested) Parquet schemas column by column, in
// declared order: column count, then each column's name, type, and
// repetition (optional/repeated/required - the same three-way
// classification pqinfo.ColumnInfo uses). It returns nil if every column
// matches, or an error describing the first mismatch found (by position).
// The returned error names columns/types/counts but not file paths - Equal
// only ever sees two *parquet.Schema values, not where they came from;
// callers that compare schemas from named files (see pqcat.Cat) wrap this
// error with %w to add that context. Like pqinfo, this only supports flat
// schemas, matching every file this CLI itself writes.
func Equal(a, b *parquet.Schema) error {
	af, bf := a.Fields(), b.Fields()
	if len(af) != len(bf) {
		return fmt.Errorf("column count %d vs %d", len(af), len(bf))
	}
	for i := range af {
		if af[i].Name() != bf[i].Name() {
			return fmt.Errorf("column %d: name %q vs %q", i, af[i].Name(), bf[i].Name())
		}
		if af[i].Type().String() != bf[i].Type().String() {
			return fmt.Errorf("column %d (%q): type %q vs %q", i, af[i].Name(), af[i].Type(), bf[i].Type())
		}
		if repetitionOf(af[i]) != repetitionOf(bf[i]) {
			return fmt.Errorf("column %d (%q): repetition %q vs %q", i, af[i].Name(), repetitionOf(af[i]), repetitionOf(bf[i]))
		}
	}
	return nil
}

// repetitionOf classifies field the same way pqinfo.ColumnInfo does
// (optional/repeated/required), for use in Equal's mismatch messages.
func repetitionOf(field parquet.Field) string {
	switch {
	case field.Optional():
		return "optional"
	case field.Repeated():
		return "repeated"
	default:
		return "required"
	}
}
