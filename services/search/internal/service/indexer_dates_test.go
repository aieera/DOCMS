// Regression: every indexed document used to carry Go's zero time.
//
// model.IndexDocument.CreatedAt was a plain time.Time with a
// non-omitempty json tag, and neither dms.document.created.v1 nor
// dms.document.reindexed.v1 shipped a created_at — so the indexer wrote
// "created_at":"0001-01-01T00:00:00Z" into OpenSearch for EVERY document.
// Search hits echoed that back, and sort_by=created_at was inert because
// the sort key was identical across the whole corpus (the Reports engine
// read the real dates straight from Postgres, which is why only search
// looked wrong).
//
// Reuses the stub OpenSearch from indexer_partial_update_test.go, which
// records the exact _source the indexer PUT.
package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnDocCreated_IndexesCreatedAtFromEvent(t *testing.T) {
	ix, stub := newStubIndexer(t)

	ix.onDocCreated(event(t, testTenant, map[string]any{
		"document_id":     "doc-dated",
		"title":           "Q3 report",
		"lifecycle_state": "active",
		"created_by_name": "Alice Example",
		"created_at":      "2026-05-20T10:30:00Z",
		"updated_at":      "2026-06-01T08:15:00Z",
	}))

	src := stub.source("doc-dated")
	require.NotNil(t, src)
	require.Equal(t, "2026-05-20T10:30:00Z", src["created_at"])
	require.Equal(t, "2026-06-01T08:15:00Z", src["updated_at"])
	require.Equal(t, "Alice Example", src["created_by_name"],
		"created_by_name feeds the author facet")
	require.Equal(t, "active", src["lifecycle_state"],
		"lifecycle_state feeds the lifecycle facet")
}

// TestOnDocCreated_OmitsCreatedAtWhenEventHasNone is the actual bug guard:
// an event with no date must leave the field ABSENT from the index doc.
// Writing the Go zero value instead is what produced 0001-01-01 on every
// hit and flattened the sort key.
func TestOnDocCreated_OmitsCreatedAtWhenEventHasNone(t *testing.T) {
	ix, stub := newStubIndexer(t)

	ix.onDocCreated(event(t, testTenant, map[string]any{
		"document_id": "doc-undated",
		"title":       "Legacy event with no dates",
	}))

	src := stub.source("doc-undated")
	require.NotNil(t, src)
	_, hasCreated := src["created_at"]
	require.False(t, hasCreated,
		"a missing date must be omitted, never written as 0001-01-01T00:00:00Z; got %v", src["created_at"])
	_, hasUpdated := src["updated_at"]
	require.False(t, hasUpdated)
}

// TestOnDocCreated_RejectsUnparseableDate: a malformed date is treated the
// same as a missing one — omitted, not coerced to the zero time.
func TestOnDocCreated_RejectsUnparseableDate(t *testing.T) {
	ix, stub := newStubIndexer(t)

	ix.onDocCreated(event(t, testTenant, map[string]any{
		"document_id": "doc-bad-date",
		"title":       "Bad date",
		"created_at":  "not-a-date",
	}))

	_, hasCreated := stub.source("doc-bad-date")["created_at"]
	require.False(t, hasCreated)
}

func TestTimeField(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool // want non-nil
	}{
		{"rfc3339", "2026-05-20T10:30:00Z", true},
		{"rfc3339 nanos", "2026-05-20T10:30:00.123456Z", true},
		{"offset", "2026-05-20T10:30:00+02:00", true},
		{"zero time", "0001-01-01T00:00:00Z", false},
		{"empty", "", false},
		{"garbage", "yesterday", false},
		{"wrong type", 1234, false},
		{"missing", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			if tc.in != nil {
				m["created_at"] = tc.in
			}
			got := timeField(m, "created_at")
			require.Equal(t, tc.want, got != nil)
		})
	}
}
