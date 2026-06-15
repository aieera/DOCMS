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

	// POST /files/search rejects an anonymous caller before reading the body.
	sr := httptest.NewRequest(http.MethodPost, "/files/search", nil)
	sw := httptest.NewRecorder()
	mux.ServeHTTP(sw, sr)
	if sw.Code != http.StatusUnauthorized {
		t.Fatalf("POST /files/search without user = %d, want 401", sw.Code)
	}
}

// TestAdminRoutesRequireAdmin: an authenticated non-admin can't reach review/sync.
func TestAdminRoutesRequireAdmin(t *testing.T) {
	b := New(nil, nil, nil, "ws", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/files/review-queue"},
		{http.MethodGet, "/files/sync/metrics"},
		{http.MethodGet, "/files/sync/backfill"},
		{http.MethodPost, "/files/sync/backfill"},
		{http.MethodGet, "/files/sync/backfill/00000000-0000-0000-0000-000000000000"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("X-ERP-User", "alice") // authed but not admin
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s as non-admin = %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

// TestBackfillRoutesRequireAuth: backfill admin routes 401 without a user (before
// any store work, so nil deps are safe).
func TestBackfillRoutesRequireAuth(t *testing.T) {
	b := New(nil, nil, nil, "ws", zerolog.Nop())
	mux := http.NewServeMux()
	b.Register(mux)

	for _, path := range []string{"/files/sync/backfill"} {
		r := httptest.NewRequest(http.MethodPost, path, nil) // no X-ERP-User
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s without user = %d, want 401", path, w.Code)
		}
	}
}
