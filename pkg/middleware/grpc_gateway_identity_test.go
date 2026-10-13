package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

func runGatewayIdentity(r *http.Request) http.Header {
	var fwd http.Header
	GatewayIdentityMetadata()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		fwd = r.Header.Clone()
	})).ServeHTTP(httptest.NewRecorder(), r)
	return fwd
}

// TestGatewayInject_SessionBeatsHeaders pins C-6: the document service's
// grpc-gateway injector turns identity into gRPC metadata, which the gRPC
// interceptors trust. It used to copy X-Tenant-ID / X-User-ID /
// X-User-Role from the HTTP request ahead of the session, and grpc-gateway
// forwards any client-sent Grpc-Metadata-* header verbatim. Only the
// authenticated context may populate identity metadata.
func TestGatewayInject_SessionBeatsHeaders(t *testing.T) {
	sessTenant, sessUser := uuid.New(), uuid.New()
	forgedTenant, forgedUser := uuid.New(), uuid.New()
	forge := func(r *http.Request) {
		r.Header.Set(TenantHeader, forgedTenant.String())
		r.Header.Set(AuthTenantHeader, forgedTenant.String())
		r.Header.Set("X-User-ID", forgedUser.String())
		r.Header.Set("X-User-Role", "admin")
		r.Header.Set("Grpc-Metadata-X-Tenant-Id", forgedTenant.String())
		r.Header.Set("Grpc-Metadata-X-User-Id", forgedUser.String())
		r.Header.Set("Grpc-Metadata-X-User-Role", "owner")
		r.Header.Set("Grpc-Metadata-X-Client-Ip", "6.6.6.6")
	}

	t.Run("session identity wins", func(t *testing.T) {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/workspaces", nil)
		forge(r)
		ctx := auth.WithUser(r.Context(), auth.UserInfo{ID: sessUser, TenantID: sessTenant, Role: "member", Email: "m@example.com"})
		ctx = auth.SetTenantID(ctx, sessTenant)
		ctx = auth.WithClientIP(ctx, "203.0.113.9")
		fwd := runGatewayIdentity(r.WithContext(ctx))
		want := map[string]string{
			"Grpc-Metadata-X-Tenant-Id": sessTenant.String(),
			"Grpc-Metadata-X-User-Id":   sessUser.String(),
			"Grpc-Metadata-X-User-Role": "member",
			"Grpc-Metadata-X-User-Name": "m@example.com",
			"Grpc-Metadata-X-Client-Ip": "203.0.113.9",
		}
		for k, v := range want {
			if got := fwd.Values(k); len(got) != 1 || got[0] != v {
				t.Errorf("%s = %v, want [%s]", k, got, v)
			}
		}
	})

	t.Run("no identity: nothing forwarded", func(t *testing.T) {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/workspaces", nil)
		forge(r)
		fwd := runGatewayIdentity(r)
		for _, k := range []string{"Grpc-Metadata-X-Tenant-Id", "Grpc-Metadata-X-User-Id", "Grpc-Metadata-X-User-Role", "Grpc-Metadata-X-Client-Ip"} {
			if v := fwd.Values(k); len(v) != 0 {
				t.Errorf("%s forwarded from client headers: %v", k, v)
			}
		}
	})
}

// TestStripGRPCMetadata: routes that hand requests to grpc-gateway without
// the identity injector (the anonymous share-link route) must still drop
// client-sent Grpc-Metadata-* headers.
func TestStripGRPCMetadata(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/shared/tok", nil)
	r.Header.Set("Grpc-Metadata-X-Tenant-Id", uuid.NewString())
	r.Header.Set("grpc-metadata-x-user-role", "admin")
	r.Header.Set("Content-Type", "application/json")
	var fwd http.Header
	StripGRPCMetadata(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { fwd = r.Header.Clone() })).
		ServeHTTP(httptest.NewRecorder(), r)
	for k := range fwd {
		if len(k) >= len("Grpc-Metadata-") && http.CanonicalHeaderKey(k)[:len("Grpc-Metadata-")] == "Grpc-Metadata-" {
			t.Errorf("client Grpc-Metadata header survived: %s", k)
		}
	}
	if fwd.Get("Content-Type") != "application/json" {
		t.Error("other headers must be preserved")
	}
}
