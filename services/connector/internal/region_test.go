// ADR 0110 — cross-region block integration test for the
// connector service.
//
// We pick the connector service for this scenario because it's the
// most likely place for a cross-region mistake: webhook providers
// (M365, Gmail, DocuSign, Stripe) push events at fixed URLs that
// won't follow a tenant's residency boundary. An event aimed at
// the US gateway must NEVER write a UAE tenant's row even if the
// network path somehow lands at the UAE cluster.
//
// Strategy:
//   * Stub the RegionResolver — no Postgres needed.
//   * Spin up a chi router with EnforceRegion installed AFTER an
//     auth middleware that stamps the tenant ID on the context.
//   * Hit the handler with two requests: one whose tenant lives
//     in the cluster's region (200 OK), one whose tenant lives
//     elsewhere (451 + the X-DMS-Region-Block header).
//
// This is the request-layer counterpart to the unit tests in
// pkg/middleware/region_http_test.go — those exercise the
// middleware in isolation; this one exercises it wired into a
// realistic handler chain.

package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	mw "github.com/vaultdms/vaultdms/pkg/middleware"
)

type fakeResolver struct{ region string }

func (f fakeResolver) Resolve(_ context.Context, _ uuid.UUID) (string, error) {
	return f.region, nil
}

// stampTenant simulates the auth middleware — pulls the tenant ID
// from a test header and seats it on the request context where
// EnforceRegion looks for it.
func stampTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("X-Test-Tenant"); h != "" {
			id, err := uuid.Parse(h)
			if err != nil {
				http.Error(w, "bad tenant id", http.StatusBadRequest)
				return
			}
			r = r.WithContext(auth.SetTenantID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

// newRouter wires EnforceRegion behind the auth-stamping shim,
// then mounts a trivial connector-like endpoint. The endpoint
// itself is irrelevant — the middleware is the unit under test.
func newRouter(clusterRegion, tenantRegion string) http.Handler {
	r := chi.NewRouter()
	r.Use(stampTenant)
	r.Use(mw.EnforceRegion(clusterRegion, fakeResolver{region: tenantRegion}, zerolog.Nop()))
	r.Post("/api/v1/connectors/email/inbound", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	})
	return r
}

func TestConnectorRegionBlock_AllowsSameRegion(t *testing.T) {
	r := newRouter("uae-central", "uae-central")
	tid := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/email/inbound", strings.NewReader("{}"))
	req.Header.Set("X-Test-Tenant", tid.String())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestConnectorRegionBlock_RejectsCrossRegion(t *testing.T) {
	// UAE cluster, US tenant — must 451 with the residency header
	// + a JSON body the caller can route on.
	r := newRouter("uae-central", "us-east-1")
	tid := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/email/inbound", strings.NewReader("{}"))
	req.Header.Set("X-Test-Tenant", tid.String())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnavailableForLegalReasons {
		t.Fatalf("expected 451; got %d", rec.Code)
	}
	hdr := rec.Header().Get("X-DMS-Region-Block")
	if !strings.Contains(hdr, "tenant-region=us-east-1") {
		t.Fatalf("X-DMS-Region-Block missing tenant region; got %q", hdr)
	}
	if !strings.Contains(hdr, "cluster-region=uae-central") {
		t.Fatalf("X-DMS-Region-Block missing cluster region; got %q", hdr)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), `"error":"residency_violation"`) {
		t.Fatalf("body should mention residency_violation; got %q", body)
	}
}

func TestConnectorRegionBlock_PreTenantRoutePassesThrough(t *testing.T) {
	// Webhook signature-verification endpoints typically run before
	// the auth middleware seats a tenant ID — those routes must
	// not 451 just because no tenant is on the context yet.
	r := newRouter("uae-central", "us-east-1")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/email/inbound", strings.NewReader("{}"))
	// No X-Test-Tenant header — context has no tenant.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-tenant route must pass; got %d", rec.Code)
	}
}
