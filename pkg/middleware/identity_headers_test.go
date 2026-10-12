package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

const testInternalKey = "test-internal-service-key"

type seenIdentity struct {
	tenant uuid.UUID
	user   auth.UserInfo
	hasUsr bool
}

func runIdentityHeaders(r *http.Request) seenIdentity {
	var s seenIdentity
	IdentityHeadersHTTP()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s.tenant, _ = auth.GetTenantID(r.Context())
		s.user, _ = auth.User(r.Context())
		_, err := auth.GetUserID(r.Context())
		s.hasUsr = err == nil
	})).ServeHTTP(httptest.NewRecorder(), r)
	return s
}

func forgedIdentityRequest(tenant, user uuid.UUID) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/steps/approve", nil)
	r.Header.Set(AuthTenantHeader, tenant.String())
	r.Header.Set(TenantHeader, tenant.String())
	r.Header.Set("X-User-ID", user.String())
	r.Header.Set("X-User-Role", "admin")
	return r
}

// TestIdentityHeadersIgnoredWithoutInternalKey pins C-6 / DEC-3: identity
// headers (X-Auth-Tenant-ID / X-Tenant-ID / X-User-*) are believed only
// from a caller holding the internal service key, and never over a
// session identity. Before the fix any request that passed the gateway
// signature (e.g. through the Vite host-mode proxy) could name any
// tenant/user/role and act as them.
func TestIdentityHeadersIgnoredWithoutInternalKey(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	tenant, user := uuid.New(), uuid.New()

	t.Run("no key: headers ignored", func(t *testing.T) {
		s := runIdentityHeaders(forgedIdentityRequest(tenant, user))
		if s.tenant != uuid.Nil || s.hasUsr {
			t.Fatalf("forged headers must be ignored, got tenant=%v user=%+v", s.tenant, s.user)
		}
	})
	t.Run("wrong key: headers ignored", func(t *testing.T) {
		r := forgedIdentityRequest(tenant, user)
		r.Header.Set(InternalServiceKeyHeader, "guess")
		if s := runIdentityHeaders(r); s.tenant != uuid.Nil || s.hasUsr {
			t.Fatalf("wrong key must not unlock headers, got tenant=%v", s.tenant)
		}
	})
	t.Run("key configured empty: headers ignored", func(t *testing.T) {
		t.Setenv("SEDOC_INTERNAL_API_KEY", "")
		r := forgedIdentityRequest(tenant, user)
		r.Header.Set(InternalServiceKeyHeader, "")
		if s := runIdentityHeaders(r); s.tenant != uuid.Nil || s.hasUsr {
			t.Fatalf("an unset internal key must never match, got tenant=%v", s.tenant)
		}
	})
	t.Run("valid key: headers honoured", func(t *testing.T) {
		r := forgedIdentityRequest(tenant, user)
		r.Header.Set(InternalServiceKeyHeader, testInternalKey)
		s := runIdentityHeaders(r)
		if s.tenant != tenant || s.user.ID != user || s.user.TenantID != tenant || s.user.Role != "admin" {
			t.Fatalf("internal caller identity not applied: tenant=%v user=%+v", s.tenant, s.user)
		}
	})
	t.Run("session wins over headers even with a valid key", func(t *testing.T) {
		sessTenant, sessUser := uuid.New(), uuid.New()
		r := forgedIdentityRequest(tenant, user)
		r.Header.Set(InternalServiceKeyHeader, testInternalKey)
		r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: sessUser, TenantID: sessTenant, Role: "member"}))
		s := runIdentityHeaders(r)
		if s.tenant != sessTenant || s.user.ID != sessUser || s.user.Role != "member" {
			t.Fatalf("session identity must not be overridden: tenant=%v user=%+v", s.tenant, s.user)
		}
	})
}
