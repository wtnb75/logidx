// Package scaffold holds the fixed-content template that `logidx scaffold`
// prints: a minimal, commented rules.yaml a new user can start editing
// immediately. Embedding the real YAML file (instead of a Go string
// literal) lets scaffold_test.go run it through rules.Load and prove the
// template itself is never broken.
package scaffold

import _ "embed"

//go:embed template.yaml
var Template string
