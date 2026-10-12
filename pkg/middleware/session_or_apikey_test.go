package middleware

// DEC-3 for SessionOrAPIKey: identity headers are honoured only with a valid
// internal-service key AND no session identity, and the internal-key path
// never fabricates a role — X-User-Role is required and must be a known role.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
)

// stubSessionResolver swaps the session lookup for the duration of a test.
func stubSessionResolver(t *testing.T, fn func(ctx context.Context, token string) (auth.UserInfo, error)) {
	t.Helper()
	prev := sessionResolver
	sessionResolver = func(ctx context.Context, _ *pgxpool.Pool, token string) (auth.UserInfo, error) {
		return fn(ctx, token)
	}
	t.Cleanup(func() { sessionResolver = prev })
}

// serveSOA runs one request through SessionOrAPIKey and reports the status
// and the identity the handler saw (nil when the handler did not run).
func serveSOA(r *http.Request) (int, *auth.UserInfo) {
	var seen *auth.UserInfo
	h := SessionOrAPIKey(SessionAuthConfig{}, "documents:read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, err := auth.User(r.Context()); err == nil {
			seen = &u
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, seen
}

func internalReq(tenant uuid.UUID, user uuid.UUID, role string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(InternalServiceKeyHeader, testInternalKey)
	r.Header.Set("X-Auth-Tenant-ID", tenant.String())
	if user != uuid.Nil {
		r.Header.Set("X-User-ID", user.String())
	}
	if role != "" {
		r.Header.Set("X-User-Role", role)
	}
	return r
}

func TestSessionOrAPIKey_SessionBeatsInternalKey(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	alice := auth.UserInfo{ID: uuid.New(), TenantID: uuid.New(), Role: "member"}
	stubSessionResolver(t, func(_ context.Context, token string) (auth.UserInfo, error) {
		if token == "alice-session" {
			return alice, nil
		}
		return auth.UserInfo{}, pgx.ErrNoRows
	})

	// A live session plus a valid internal key naming another tenant, user
	// and an admin role: the session identity wins, the headers are ignored.
	otherTenant, otherUser := uuid.New(), uuid.New()
	for name, attach := range map[string]func(r *http.Request){
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "dms_session", Value: "alice-session"}) },
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer alice-session") },
	} {
		t.Run(name, func(t *testing.T) {
			r := internalReq(otherTenant, otherUser, "admin")
			attach(r)
			code, seen := serveSOA(r)
			if code != http.StatusOK || seen == nil {
				t.Fatalf("code = %d, seen = %v; want 200 with the session identity", code, seen)
			}
			if seen.ID != alice.ID || seen.TenantID != alice.TenantID || seen.Role != "member" {
				t.Fatalf("identity = %+v; want the session user %+v (headers must be ignored)", *seen, alice)
			}
		})
	}

	t.Run("no live session falls back to the internal key", func(t *testing.T) {
		r := internalReq(otherTenant, otherUser, "member")
		r.AddCookie(&http.Cookie{Name: "dms_session", Value: "stale"})
		code, seen := serveSOA(r)
		if code != http.StatusOK || seen == nil || seen.TenantID != otherTenant || seen.ID != otherUser {
			t.Fatalf("code = %d, seen = %v; want 200 with the internal identity", code, seen)
		}
	})

	t.Run("session infra error is 503, never the header identity", func(t *testing.T) {
		stubSessionResolver(t, func(context.Context, string) (auth.UserInfo, error) {
			return auth.UserInfo{}, errors.New("pool exhausted")
		})
		r := internalReq(otherTenant, otherUser, "admin")
		r.AddCookie(&http.Cookie{Name: "dms_session", Value: "alice-session"})
		code, seen := serveSOA(r)
		if code != http.StatusServiceUnavailable || seen != nil {
			t.Fatalf("code = %d, seen = %v; want 503 and no identity", code, seen)
		}
	})
}

func TestSessionOrAPIKey_InternalKeyRequiresRole(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	stubSessionResolver(t, func(context.Context, string) (auth.UserInfo, error) {
		t.Fatal("no session token was sent; the resolver must not run")
		return auth.UserInfo{}, nil
	})
	tenant, user := uuid.New(), uuid.New()

	for _, role := range []string{"", "superuser", "ADMIN", " admin"} {
		t.Run("rejects role "+role, func(t *testing.T) {
			code, seen := serveSOA(internalReq(tenant, user, role))
			if code != http.StatusUnauthorized || seen != nil {
				t.Fatalf("role %q: code = %d, seen = %v; want 401 and no identity (never a default of admin)", role, code, seen)
			}
		})
	}

	for _, role := range []string{"owner", "admin", "member", "guest", "viewer", "compliance_officer", "api_key"} {
		t.Run("accepts role "+role, func(t *testing.T) {
			code, seen := serveSOA(internalReq(tenant, user, role))
			if code != http.StatusOK || seen == nil {
				t.Fatalf("role %q: code = %d, seen = %v; want 200", role, code, seen)
			}
			if seen.Role != role || seen.TenantID != tenant || seen.ID != user {
				t.Fatalf("identity = %+v", *seen)
			}
		})
	}

	t.Run("wrong key is 401 even with a role", func(t *testing.T) {
		r := internalReq(tenant, user, "member")
		r.Header.Set(InternalServiceKeyHeader, "wrong")
		if code, seen := serveSOA(r); code != http.StatusUnauthorized || seen != nil {
			t.Fatalf("code = %d, seen = %v; want 401", code, seen)
		}
	})

	t.Run("missing tenant is 401", func(t *testing.T) {
		r := internalReq(tenant, user, "member")
		r.Header.Del("X-Auth-Tenant-ID")
		if code, seen := serveSOA(r); code != http.StatusUnauthorized || seen != nil {
			t.Fatalf("code = %d, seen = %v; want 401", code, seen)
		}
	})
}
