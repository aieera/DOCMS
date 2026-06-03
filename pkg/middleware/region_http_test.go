package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
)

// stubResolver lets us return a hard-coded region (or an error) from
// the middleware without standing up Postgres. Keyed by tenant UUID
// so a single test can simulate several tenants.
type stubResolver struct {
	byTenant map[uuid.UUID]string
	err      error
}

func (s *stubResolver) Resolve(_ context.Context, id uuid.UUID) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	r, ok := s.byTenant[id]
	if !ok {
		return "", ErrRegionUnknown
	}
	return r, nil
}

// withTenant attaches the given tenant ID to the request context the
// same way the auth middleware does in production.
func withTenant(r *http.Request, tid uuid.UUID) *http.Request {
	ctx := auth.SetTenantID(r.Context(), tid)
	return r.WithContext(ctx)
}

func makeHandler(t *testing.T, clusterRegion string, resolver RegionResolver) http.Handler {
	t.Helper()
	mw := EnforceRegion(clusterRegion, resolver, zerolog.Nop())
	return mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"served":true}`))
	}))
}

func TestEnforceRegion_SameRegion_AllowsRequest(t *testing.T) {
	tid := uuid.New()
	h := makeHandler(t, "uae-central", &stubResolver{
		byTenant: map[uuid.UUID]string{tid: "uae-central"},
	})
	req := withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil), tid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same region must pass; got %d", rec.Code)
	}
}

func TestEnforceRegion_DifferentRegion_Blocks451(t *testing.T) {
	tid := uuid.New()
	h := makeHandler(t, "us-east-1", &stubResolver{
		byTenant: map[uuid.UUID]string{tid: "uae-central"},
	})
	req := withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil), tid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("cross-region must return 451; got %d", rec.Code)
	}
	hdr := rec.Header().Get("X-DMS-Region-Block")
	if !strings.Contains(hdr, "tenant-region=uae-central") || !strings.Contains(hdr, "cluster-region=us-east-1") {
		t.Fatalf("X-DMS-Region-Block missing region pair; got %q", hdr)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `"error":"residency_violation"`) {
		t.Fatalf("body should mention residency_violation; got %q", body)
	}
}

func TestEnforceRegion_NoTenantOnContext_PassesThrough(t *testing.T) {
	// /auth/login and similar pre-tenant routes don't carry a tenant
	// in the context. The middleware must NOT 404 those — they're
	// not residency-scoped.
	h := makeHandler(t, "uae-central", &stubResolver{byTenant: map[uuid.UUID]string{}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-tenant route must pass; got %d", rec.Code)
	}
}

func TestEnforceRegion_UnknownTenant_Returns404(t *testing.T) {
	// An unknown tenant ID is 404, NOT 451. Leaking "this tenant
	// exists but is in another region" is a worse compliance outcome
	// than a flat not-found.
	tid := uuid.New()
	h := makeHandler(t, "uae-central", &stubResolver{byTenant: map[uuid.UUID]string{}})
	req := withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil), tid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant must 404; got %d", rec.Code)
	}
}

func TestEnforceRegion_ResolverError_Returns500(t *testing.T) {
	tid := uuid.New()
	h := makeHandler(t, "uae-central", &stubResolver{err: errors.New("pg: connection refused")})
	req := withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil), tid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("transient resolver failure must 500; got %d", rec.Code)
	}
}

func TestEnforceRegion_EmptyClusterRegion_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("constructing the middleware with an empty cluster region must panic")
		}
	}()
	_ = EnforceRegion("   ", &stubResolver{}, zerolog.Nop())
}

func TestEnforceRegion_CaseAndWhitespaceInsensitive(t *testing.T) {
	tid := uuid.New()
	h := makeHandler(t, "  UAE-Central ", &stubResolver{
		byTenant: map[uuid.UUID]string{tid: " uae-central "},
	})
	req := withTenant(httptest.NewRequest(http.MethodGet, "/api/v1/documents", nil), tid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("case/whitespace should not block; got %d", rec.Code)
	}
}
