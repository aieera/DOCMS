// Regression: a sparse dms.document.updated.v1 event must NEVER
// full-replace the OpenSearch document.
//
// The document service's updated event carries only {document_id,
// changed_fields, updated_by} (+ readable_by on folder moves) — no
// content, no ACL. The old indexer routed it through the same
// full-replace path as created events, so ANY metadata edit rebuilt the
// index doc from the sparse payload: readable_by and content were wiped
// and the doc vanished from every user's results (empty readable_by
// matches nobody's ACL filter).
//
// The stub below emulates just enough OpenSearch to tell a full PUT
// (_doc — replace) from a partial update (_update — merge), which is
// exactly the distinction under test. Query-side ACL semantics are real
// OpenSearch territory — covered by the integration suite.
package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/search/internal/opensearch"
)

// stubOS is an in-memory OpenSearch lookalike: PUT /_doc replaces the
// source; POST /_update merges the `doc` fields; both per document id.
type stubOS struct {
	mu   sync.Mutex
	docs map[string]map[string]any // doc id → _source
}

func newStubOS() *stubOS { return &stubOS{docs: map[string]map[string]any{}} }

func (s *stubOS) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_index_template/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead: // EnsureIndex probe
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && len(parts) == 3 && parts[1] == "_doc":
			body, _ := io.ReadAll(r.Body)
			var src map[string]any
			_ = json.Unmarshal(body, &src)
			s.docs[parts[2]] = src // FULL REPLACE
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && len(parts) == 3 && parts[1] == "_update":
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Doc map[string]any `json:"doc"`
			}
			_ = json.Unmarshal(body, &req)
			cur, ok := s.docs[parts[2]]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"type":"document_missing_exception"}}`))
				return
			}
			for k, v := range req.Doc { // MERGE
				cur[k] = v
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	})
}

func (s *stubOS) source(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docs[id]
}

func newStubIndexer(t *testing.T) (*Indexer, *stubOS) {
	t.Helper()
	stub := newStubOS()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	osc, err := opensearch.NewReal(t.Context(), opensearch.Config{URL: srv.URL, Logger: zerolog.Nop()})
	require.NoError(t, err)
	svc := New(Config{OS: osc, Logger: zerolog.Nop()})
	return NewIndexer(svc, nil, nil, zerolog.Nop()), stub
}

// event wraps a data payload in the outbox CloudEvents envelope shape
// the indexer receives (tenant id rides the `tenantid` extension).
func event(t *testing.T, tenantID string, data map[string]any) *nats.Msg {
	t.Helper()
	body, err := json.Marshal(map[string]any{"tenantid": tenantID, "data": data})
	require.NoError(t, err)
	return &nats.Msg{Data: body}
}

const testTenant = "11111111-1111-1111-1111-111111111111"

func seedFullDoc(t *testing.T, ix *Indexer, docID string) {
	t.Helper()
	ix.onDocCreated(event(t, testTenant, map[string]any{
		"document_id":        docID,
		"workspace_id":       "ws-1",
		"folder_id":          "f-1",
		"title":              "Q3 financial report",
		"content":            "revenue grew twelve percent quarter over quarter",
		"content_snippet":    "revenue grew twelve percent",
		"lifecycle_state":    "active",
		"readable_by":        []any{"user-allowed", "group-fin"},
		"readable_by_users":  []any{"user-allowed"},
		"readable_by_groups": []any{"group-fin"},
	}))
}

// TestDocUpdated_SparseEventDoesNotWipeIndex is the core regression:
// a title-only update (exactly what the document service emits) must
// leave content + readable_by intact and apply the new title.
func TestDocUpdated_SparseEventDoesNotWipeIndex(t *testing.T) {
	ix, stub := newStubIndexer(t)
	seedFullDoc(t, ix, "doc-1")

	// The updated.v1 payload as the document service emits it after
	// this fix: changed fields marked explicitly WITH their new values.
	ix.onDocUpdated(event(t, testTenant, map[string]any{
		"document_id":    "doc-1",
		"changed_fields": []any{"title"},
		"changed":        map[string]any{"title": "Q3 financial report (final)"},
		"updated_by":     "user-editor",
	}))

	src := stub.source("doc-1")
	require.NotNil(t, src, "doc must still exist")
	require.Equal(t, "Q3 financial report (final)", src["title"], "title change must be applied")
	require.Equal(t, "revenue grew twelve percent quarter over quarter", src["content"],
		"content must survive a metadata-only update")
	require.ElementsMatch(t, []any{"user-allowed", "group-fin"}, src["readable_by"],
		"readable_by (the ACL filter) must survive a metadata-only update")
	require.ElementsMatch(t, []any{"user-allowed"}, src["readable_by_users"])
}

// TestDocUpdated_LegacySparseEvent_NoValues_NoWipe pins the behavior for
// events from an old document service that carries changed_fields but no
// values: nothing to apply → nothing may be touched (ack, no wipe).
func TestDocUpdated_LegacySparseEvent_NoValues_NoWipe(t *testing.T) {
	ix, stub := newStubIndexer(t)
	seedFullDoc(t, ix, "doc-2")

	ix.onDocUpdated(event(t, testTenant, map[string]any{
		"document_id":    "doc-2",
		"changed_fields": []any{"title"},
		"updated_by":     "user-editor",
	}))

	src := stub.source("doc-2")
	require.Equal(t, "Q3 financial report", src["title"], "no value shipped → no change")
	require.NotEmpty(t, src["content"])
	require.NotEmpty(t, src["readable_by"])
}

// TestDocUpdated_FolderMoveCarriesACL: updated events on folder moves
// ship readable_by (existing FIX-4 contract) — the partial path must
// propagate them without touching content.
func TestDocUpdated_FolderMoveCarriesACL(t *testing.T) {
	ix, stub := newStubIndexer(t)
	seedFullDoc(t, ix, "doc-3")

	ix.onDocUpdated(event(t, testTenant, map[string]any{
		"document_id":       "doc-3",
		"changed_fields":    []any{"folder_id"},
		"changed":           map[string]any{"folder_id": "f-2"},
		"readable_by":       []any{"user-new-scope"},
		"readable_by_users": []any{"user-new-scope"},
		"updated_by":        "user-mover",
	}))

	src := stub.source("doc-3")
	require.Equal(t, "f-2", src["folder_id"])
	require.ElementsMatch(t, []any{"user-new-scope"}, src["readable_by"])
	require.NotEmpty(t, src["content"], "ACL rewrite must not wipe content")
}

// TestDocUpdated_MissingIndexDoc_Skips: partial update against a doc the
// index never saw (backfill window) must not loop — mirror the
// version.uploaded 404-skip semantics.
func TestDocUpdated_MissingIndexDoc_Skips(t *testing.T) {
	ix, _ := newStubIndexer(t)
	// No seed — the update targets a nonexistent doc; the handler must
	// swallow the 404 (no panic, no retry storm). Nothing to assert
	// beyond "returns without side effects".
	ix.onDocUpdated(event(t, testTenant, map[string]any{
		"document_id":    "doc-ghost",
		"changed_fields": []any{"title"},
		"changed":        map[string]any{"title": "x"},
	}))
}

// TestDocReindexed_FullReplaceRepairsWipedDoc: the reconcile event
// (dms.document.reindexed.v1) carries the authoritative full payload and
// MAY full-replace — that's how already-wiped docs get repaired.
func TestDocReindexed_FullReplaceRepairsWipedDoc(t *testing.T) {
	ix, stub := newStubIndexer(t)
	// Simulate an already-wiped doc (what the old bug left behind).
	stub.mu.Lock()
	stub.docs["doc-4"] = map[string]any{
		"tenant_id": testTenant, "document_id": "doc-4",
		"title": "", "content": "", "readable_by": []any{},
	}
	stub.mu.Unlock()

	ix.onDocCreated(event(t, testTenant, map[string]any{
		"document_id":       "doc-4",
		"title":             "Recovered doc",
		"content":           "full text restored from ocr_results",
		"readable_by":       []any{"user-allowed"},
		"readable_by_users": []any{"user-allowed"},
		"lifecycle_state":   "active",
	}))

	src := stub.source("doc-4")
	require.Equal(t, "Recovered doc", src["title"])
	require.Equal(t, "full text restored from ocr_results", src["content"])
	require.ElementsMatch(t, []any{"user-allowed"}, src["readable_by"])
}
