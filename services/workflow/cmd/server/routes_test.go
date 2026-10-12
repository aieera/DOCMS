package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/workflow/internal/handler"
)

const (
	testGatewaySecret = "test-gateway-secret-0123456789abcdef"
	testInternalKey   = "internal-key-for-tests"
)

// testRouter builds the production router with no database and no service:
// every request in these tests must be decided by the middleware before a
// handler that touches the (nil) service runs.
func testRouter(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	triggers := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	return newRouter(nil, handler.New(nil, zerolog.Nop()), triggers)
}

// serve runs one request and converts a handler panic (the nil service being
// reached) into a 599 so the assertion reports which route leaked through.
func serve(h http.Handler, r *http.Request) (code int) {
	defer func() {
		if rec := recover(); rec != nil {
			code = 599
		}
	}()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func forged(r *http.Request, tenant, user string) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Gateway-Signature", testGatewaySecret)
	r.Header.Set("X-Tenant-ID", tenant)
	r.Header.Set("X-Auth-Tenant-ID", tenant)
	r.Header.Set("X-User-ID", user)
	r.Header.Set("X-User-Role", "admin")
}

// C-6: a request that carries a valid gateway signature (as the Vite
// host-mode proxy adds) and forged identity headers but no session cookie
// must be rejected with 401 on every workflow route. Before the fix the
// service mounted only RequireGatewaySignature → IdentityHeadersHTTP, so the
// forged headers were the caller's identity: anyone who knew a tenant,
// instance and approver id could approve steps as that approver.
func TestWorkflowAPI_NoSession401WithForgedHeaders(t *testing.T) {
	router := testRouter(t)
	tenant, user := uuid.NewString(), uuid.NewString()
	id, docID := uuid.NewString(), uuid.NewString()

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/workflows/definitions"},
		{http.MethodPost, "/api/v1/workflows/definitions"},
		{http.MethodGet, "/api/v1/workflows/definitions/" + id},
		{http.MethodPut, "/api/v1/workflows/definitions/" + id},
		{http.MethodDelete, "/api/v1/workflows/definitions/" + id},
		{http.MethodPost, "/api/v1/workflows/definitions/" + id + "/visibility"},
		{http.MethodGet, "/api/v1/workflows/definitions/" + id + "/grants"},
		{http.MethodPost, "/api/v1/workflows/definitions/" + id + "/grants"},
		{http.MethodDelete, "/api/v1/workflows/definitions/" + id + "/grants/user/" + user},
		{http.MethodPost, "/api/v1/workflows/instances"},
		{http.MethodGet, "/api/v1/workflows/instances"},
		{http.MethodGet, "/api/v1/workflows/instances/" + id},
		{http.MethodGet, "/api/v1/workflows/instances/" + id + "/timeline"},
		{http.MethodPost, "/api/v1/workflows/instances/" + id + "/signal"},
		{http.MethodPost, "/api/v1/workflows/instances/" + id + "/cancel"},
		{http.MethodPost, "/api/v1/workflows/instances/" + id + "/recall"},
		{http.MethodGet, "/api/v1/workflows/documents/" + docID},
		{http.MethodGet, "/api/v1/workflows/tasks/mine"},
		{http.MethodGet, "/api/v1/workflows/tasks"},
		{http.MethodPost, "/api/v1/workflows/delegations"},
		{http.MethodGet, "/api/v1/workflows/delegations"},
		{http.MethodDelete, "/api/v1/workflows/delegations/" + id},
		// Unknown paths under the API must not leak a 404-vs-401 oracle.
		{http.MethodGet, "/api/v1/workflows/nope"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{"outcome":"approve"}`))
			forged(r, tenant, user)
			if got := serve(router, r); got != http.StatusUnauthorized {
				t.Fatalf("%s %s without a session: got %d, want 401", rt.method, rt.path, got)
			}
		})
	}
}

// Positive control: a trusted internal caller (valid X-Internal-Service-Key)
// still reaches the handler with the identity it names, so the 401s above
// come from the missing session, not from a route that is unreachable.
func TestWorkflowAPI_InternalKeyReachesHandler(t *testing.T) {
	router := testRouter(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/tasks?status=bogus", nil)
	forged(r, uuid.NewString(), uuid.NewString())
	r.Header.Set("X-Internal-Service-Key", testInternalKey)
	// The handler validates the status filter before touching the service:
	// 400 proves it ran with a tenant and user on the context.
	if got := serve(router, r); got != http.StatusBadRequest {
		t.Fatalf("internal caller: got %d, want 400 from the handler", got)
	}
}

// The iPaaS trigger keeps its own API-key gate and never accepts forged
// identity headers either.
func TestWorkflowAPI_TriggerRouteKeepsItsOwnAuth(t *testing.T) {
	router := testRouter(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/triggers/workflows/completed", nil)
	if got := serve(router, r); got != http.StatusTeapot {
		t.Fatalf("trigger route: got %d, want the trigger handler (418)", got)
	}
}
