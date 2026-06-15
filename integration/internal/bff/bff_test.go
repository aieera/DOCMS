package bff

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

// TestUnauthenticatedRejected: a request with no X-ERP-User is 401 before any
// store/authz/SeDoc work — so nil deps are safe here.
func TestUnauthenticatedRejected(t *testing.T) {
	b := New(nil, nil, nil, "ws", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)

	for _, path := range []string{
		"/files/customers/CUST-1/tree",
		"/files/documents/doc-1",
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil) // no X-ERP-User
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without user = %d, want 401", path, w.Code)
		}
	}
}

// TestAdminRoutesRequireAdmin: an authenticated non-admin can't reach review/sync.
func TestAdminRoutesRequireAdmin(t *testing.T) {
	b := New(nil, nil, nil, "ws", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)

	r := httptest.NewRequest(http.MethodGet, "/files/review-queue", nil)
	r.Header.Set("X-ERP-User", "alice") // authed but not admin
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("review-queue as non-admin = %d, want 403", w.Code)
	}
}
