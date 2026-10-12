package exec

import (
	"context"
	"testing"
)

type recordingResolvers struct {
	stubResolvers
	limit           int
	cursor          string
	includeResolved bool
}

func (r *recordingResolvers) DocumentVersions(_ context.Context, _ any, limit int, cursor string) (any, error) {
	r.limit, r.cursor = limit, cursor
	return nil, nil
}

func (r *recordingResolvers) DocumentComments(_ context.Context, _ any, inc bool, _ int, _ string) (any, error) {
	r.includeResolved = inc
	return nil, nil
}

// R-7: arguments on child fields that reference operation variables must be
// resolved against those variables, not against nil.
func TestExecutor_ChildArgVariable(t *testing.T) {
	rec := &recordingResolvers{}
	e := &Executor{Schema: MustLoadSchema(), Resolvers: rec}
	q := `query Doc($lim: Int, $cur: String, $inc: Boolean) {
	  document(id: "doc-1") {
	    id
	    versions(limit: $lim, cursor: $cur) { nextCursor }
	    comments(includeResolved: $inc) { nextCursor }
	  }
	}`
	// JSON-decoded variables: numbers arrive as float64.
	vars := map[string]any{"lim": float64(7), "cur": "abc", "inc": true}
	res := e.Execute(context.Background(), q, vars, "")
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if rec.limit != 7 || rec.cursor != "abc" || !rec.includeResolved {
		t.Fatalf("child args = limit %d cursor %q includeResolved %v; want 7 \"abc\" true",
			rec.limit, rec.cursor, rec.includeResolved)
	}
}
