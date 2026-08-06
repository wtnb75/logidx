package schema

import (
	"fmt"
	"sort"

	"github.com/parquet-go/parquet-go"

	"logidx/internal/rules"
)

// Built holds a derived Parquet schema together with the sorted field-name
// order used to build it, so callers can construct rows in matching order.
type Built struct {
	Schema  *parquet.Schema
	Columns []string
}

// Build derives a Parquet schema for the given rule name from its field
// definitions. Columns are ordered alphabetically by field name for
// deterministic output.
func Build(name string, fields map[string]rules.Field) (*Built, error) {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names)

	group := parquet.Group{}
	for _, n := range names {
		node, err := nodeForType(fields[n].Type)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", n, err)
		}
		group[n] = parquet.Required(node)
	}

	return &Built{
		Schema:  parquet.NewSchema(name, group),
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

func nodeForType(t string) (parquet.Node, error) {
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
