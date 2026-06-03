// Package exec is the v1 hand-rolled GraphQL executor for the
// SeDoc read API (ADR 0074).
//
// The full gqlgen pipeline is wired (see gqlgen.yml + go:generate
// directive in cmd/server/main.go) but the runtime today uses a
// minimal gqlparser-backed dispatcher so the service can ship
// without the multi-thousand-line generated bundle. The shape of
// the resolver interface here matches what gqlgen produces, so
// migrating to fully-generated code later is a swap, not a
// rewrite.
//
// The executor walks the parsed selection set field-by-field and
// calls into Resolvers (an interface implemented in
// internal/resolver). Field marshaling is reflection over the
// model package's `json:"..."` tags; gqlparser has already
// validated that every requested field exists on the type by the
// time we get here, so reflection misses are programmer errors,
// not user input.
package exec

import (
	_ "embed"
	"fmt"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

//go:embed schema.graphqls
var schemaSDL string

// MustLoadSchema parses the embedded SDL once at boot. Panics on a
// schema error — that's a deploy-blocking bug, not a runtime
// concern.
func MustLoadSchema() *ast.Schema {
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "vaultdms.graphqls", Input: schemaSDL})
	if err != nil {
		panic(fmt.Sprintf("schema load: %v", err))
	}
	return s
}
