package activities

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	testGatewaySecret = "test-gateway-secret-0123456789abcdef"
	testInternalKey   = "internal-key-for-tests"
)

// capturedCall records the headers of the one request a stub service saw.
type capturedCall struct {
	path   string
	header http.Header
}

// stubService answers every request with body and records what it saw.
func stubService(t *testing.T, body any) (*httptest.Server, *capturedCall) {
	t.Helper()
	got := &capturedCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// requireServiceAuth asserts the credentials a workflow → sibling-service
// call must carry under DEC-3: the gateway signature (the receiver sits
// behind RequireGatewaySignature) AND the internal-service key (without it
// the receiver ignores X-Auth-Tenant-ID / X-User-*), plus the identity.
func requireServiceAuth(t *testing.T, got *capturedCall, tenant, user string) {
	t.Helper()
	if got.header == nil {
		t.Fatal("the stub service was never called")
	}
	checks := map[string]string{
		"X-Gateway-Signature":    testGatewaySecret,
		"X-Internal-Service-Key": testInternalKey,
		"X-Auth-Tenant-ID":       tenant,
	}
	if user != "" {
		checks["X-User-ID"] = user
	}
	for h, want := range checks {
		if v := got.header.Get(h); v != want {
			t.Errorf("%s %s: header %s = %q, want %q", "call to", got.path, h, v, want)
		}
	}
}

func testActivities(urls map[string]string) *Activities {
	return &Activities{ServiceURLs: urls, InternalKey: testInternalKey, Log: zerolog.Nop()}
}

// C-6 / DEC-3: the saved-search alert runs the owner's search on the search
// service. It used to send only the gateway signature and the identity
// headers; once IdentityHeadersHTTP honours those headers only with the
// internal-service key, the call lost its identity and every alert failed.
func TestRunSavedSearchAlert_SendsInternalKey(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	tenant, owner := uuid.NewString(), uuid.NewString()
	srv, got := stubService(t, map[string]any{"results": []map[string]string{{"document_id": "d1"}}})

	ids, err := testActivities(map[string]string{"search": srv.URL}).RunSavedSearchAlert(context.Background(), RunSavedSearchInput{
		SavedSearchID: uuid.NewString(), TenantID: tenant, OwnerUserID: owner, Query: "q",
	})
	if err != nil {
		t.Fatalf("RunSavedSearchAlert: %v", err)
	}
	if len(ids) != 1 || ids[0] != "d1" {
		t.Fatalf("ids = %v", ids)
	}
	requireServiceAuth(t, got, tenant, owner)
	// The alert acts for the owner, never as an administrator: SessionOrAPIKey
	// defaults a role-less internal caller to admin.
	if role := got.header.Get("X-User-Role"); role == "" || role == "admin" || role == "owner" {
		t.Errorf("X-User-Role = %q, want a non-admin role", role)
	}
}

// The workflow-owned PAdES seal (ADR 0025) calls the signature service's
// internal endpoint; it must carry the gateway signature as well as the key.
func TestSealSignatureCeremony_SendsServiceAuth(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	tenant, initiator := uuid.NewString(), uuid.NewString()
	srv, got := stubService(t, map[string]any{"new_version_id": "v2", "level": "B-LT"})

	if _, err := testActivities(map[string]string{"signature": srv.URL}).SealSignatureCeremony(context.Background(),
		tenant, uuid.NewString(), uuid.NewString(), uuid.NewString(), initiator); err != nil {
		t.Fatalf("SealSignatureCeremony: %v", err)
	}
	requireServiceAuth(t, got, tenant, initiator)
}

// A worker started without SEDOC_INTERNAL_API_KEY must fail the call with a
// clear error instead of sending an empty key and getting an opaque 401.
func TestServiceCall_MissingInternalKeyFailsClearly(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	srv, got := stubService(t, map[string]any{"results": []any{}})
	a := testActivities(map[string]string{"search": srv.URL})
	a.InternalKey = ""

	_, err := a.RunSavedSearchAlert(context.Background(), RunSavedSearchInput{
		TenantID: uuid.NewString(), OwnerUserID: uuid.NewString(), Query: "q",
	})
	if err == nil || !strings.Contains(err.Error(), "SEDOC_INTERNAL_API_KEY") {
		t.Fatalf("err = %v, want a missing SEDOC_INTERNAL_API_KEY error", err)
	}
	if got.header != nil {
		t.Fatal("the call must not be sent without the internal key")
	}
}
