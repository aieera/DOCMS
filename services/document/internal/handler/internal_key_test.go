package handler

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// D-23: the internal-key check compared secrets with ==, which returns at
// the first differing byte. It must reject every non-matching key and do
// the comparison through crypto/subtle.
func TestCheckInternalKey_ConstantTime(t *testing.T) {
	const key = "s3cr3t-internal-key"
	t.Setenv("SEDOC_INTERNAL_API_KEY", key)
	check := func(authz string) bool {
		r := httptest.NewRequestWithContext(context.Background(), "POST", "/internal/v1/retention/sweep", nil)
		if authz != "" {
			r.Header.Set("Authorization", authz)
		}
		return checkInternalKey(r)
	}
	require.True(t, check("Bearer "+key))
	for _, bad := range []string{"", "Bearer ", "Bearer s3cr3t-internal-kez", "Bearer " + key[:5], "Bearer " + key + "x", key + "-"} {
		require.False(t, check(bad), "%q", bad)
	}
	t.Setenv("SEDOC_INTERNAL_API_KEY", "")
	require.False(t, check("Bearer "), "an unset key never authorizes")

	// The comparison itself goes through subtle.ConstantTimeCompare, and
	// no == / != touches the secret.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "retention_sweep_handler.go", nil, 0)
	require.NoError(t, err)
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "checkInternalKey" {
			fn = fd
		}
	}
	require.NotNil(t, fn)
	usesSubtle := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "subtle" && x.Sel.Name == "ConstantTimeCompare" {
				usesSubtle = true
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				for _, side := range []ast.Expr{x.X, x.Y} {
					if id, ok := side.(*ast.Ident); ok && (id.Name == "want" || id.Name == "got") {
						t.Errorf("secret compared with %s at %s", x.Op, fset.Position(x.Pos()))
					}
				}
			}
		}
		return true
	})
	require.True(t, usesSubtle, "checkInternalKey must use subtle.ConstantTimeCompare")
}
