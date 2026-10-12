package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aieera/sedoc/pkg/auth"
)

// TenantHTTP + TenantInterceptor are the boundary between the network and
// RLS. Any bypass here leaks data across tenants.

func TestTenantHTTP_RejectsMissingHeader(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := TenantHTTP(nil) // skip pool path
	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if called {
		t.Error("downstream ran without tenant")
	}
}

func TestTenantHTTP_RejectsMalformedHeader(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mw := TenantHTTP(nil)
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(TenantHeader, "not-a-uuid")
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestTenantHTTP_RejectsNilUUID(t *testing.T) {
	// Zero UUID is a sentinel meaning "no tenant" — must be refused.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(TenantHeader, "00000000-0000-0000-0000-000000000000")
	w := httptest.NewRecorder()
	TenantHTTP(nil)(next).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestTenantHTTP_PutsTenantOnContext: an internal caller (service key) that
// names its tenant in the header gets it on ctx. (Header-only tenants from
// callers WITHOUT the key are refused since C-6 — see
// TestTenantHTTP_HeaderMismatch403.)
func TestTenantHTTP_PutsTenantOnContext(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	want := uuid.New()
	var seen uuid.UUID
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, err := auth.GetTenantID(r.Context())
		if err != nil {
			t.Errorf("GetTenantID: %v", err)
		}
		seen = tid
	})
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(TenantHeader, want.String())
	req.Header.Set(InternalServiceKeyHeader, testInternalKey)
	w := httptest.NewRecorder()
	TenantHTTP(nil)(next).ServeHTTP(w, req)

	if seen != want {
		t.Errorf("downstream ctx tenant: got %v, want %v", seen, want)
	}
}

// TestTenantHTTP_DualReadHeaders pins the Track 3 migration: TenantHTTP accepts
// the canonical X-Auth-Tenant-ID, still accepts the legacy X-Tenant-ID, and
// prefers the canonical when both are present (so the FE can drop the legacy
// dual-write without breaking any reader). Since C-6 header tenants are only
// honoured from internal callers, so the requests carry the service key.
func TestTenantHTTP_DualReadHeaders(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	canonical := uuid.New()
	legacy := uuid.New()
	run := func(set func(r *http.Request)) (uuid.UUID, int) {
		var seen uuid.UUID
		next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = auth.GetTenantID(r.Context())
		})
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set(InternalServiceKeyHeader, testInternalKey)
		set(req)
		w := httptest.NewRecorder()
		TenantHTTP(nil)(next).ServeHTTP(w, req)
		return seen, w.Code
	}

	// Canonical only → used.
	if got, code := run(func(r *http.Request) { r.Header.Set(AuthTenantHeader, canonical.String()) }); got != canonical || code != http.StatusOK {
		t.Errorf("canonical-only: got %v (code %d), want %v", got, code, canonical)
	}
	// Legacy only → still used (backward compatible).
	if got, code := run(func(r *http.Request) { r.Header.Set(TenantHeader, legacy.String()) }); got != legacy || code != http.StatusOK {
		t.Errorf("legacy-only: got %v (code %d), want %v", got, code, legacy)
	}
	// Both present → canonical wins.
	if got, _ := run(func(r *http.Request) {
		r.Header.Set(AuthTenantHeader, canonical.String())
		r.Header.Set(TenantHeader, legacy.String())
	}); got != canonical {
		t.Errorf("both present: got %v, want canonical %v", got, canonical)
	}
}

// ---- gRPC interceptor ---------------------------------------------------

func TestTenantInterceptor_RejectsMissingMetadata(t *testing.T) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}
	ic := TenantInterceptor(nil)
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err == nil {
		t.Fatal("missing tenant should error")
	}
	if called {
		t.Error("handler ran without tenant")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unauthenticated {
		t.Errorf("grpc code: got %v, want Unauthenticated", st.Code())
	}
}

func TestTenantInterceptor_RejectsMalformedTenant(t *testing.T) {
	md := metadata.Pairs(TenantMetadataKey, "not-a-uuid")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	handler := func(ctx context.Context, req any) (any, error) { return nil, nil }

	_, err := TenantInterceptor(nil)(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err == nil {
		t.Fatal("malformed tenant must fail")
	}
}

func TestTenantInterceptor_ValidTenantReachesHandler(t *testing.T) {
	tid := uuid.New()
	md := metadata.Pairs(TenantMetadataKey, tid.String())
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var got uuid.UUID
	handler := func(ctx context.Context, req any) (any, error) {
		g, err := auth.GetTenantID(ctx)
		if err != nil {
			t.Errorf("tenant not on ctx: %v", err)
		}
		got = g
		return "ok", nil
	}
	resp, err := TenantInterceptor(nil)(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp != "ok" {
		t.Errorf("handler didn't return: %v", resp)
	}
	if got != tid {
		t.Errorf("tenant mismatch: got %v, want %v", got, tid)
	}
}

// TestTenantHTTP_HeaderMismatch403 pins C-6 / DEC-3: TenantHTTP takes the
// tenant from the authenticated context first; a tenant header that names
// a different tenant is refused with 403 (it used to override the
// session). Without an authenticated identity a header tenant is honoured
// only from an internal caller holding the service key — a cookieless
// request with a forged X-Tenant-ID gets 401.
func TestTenantHTTP_HeaderMismatch403(t *testing.T) {
	t.Setenv("SEDOC_INTERNAL_API_KEY", testInternalKey)
	sessTenant, other := uuid.New(), uuid.New()
	run := func(r *http.Request) (int, uuid.UUID) {
		var seen uuid.UUID
		w := httptest.NewRecorder()
		TenantHTTP(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = auth.GetTenantID(r.Context())
		})).ServeHTTP(w, r)
		return w.Code, seen
	}
	withSession := func(r *http.Request) *http.Request {
		return r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: uuid.New(), TenantID: sessTenant, Role: "member"}))
	}

	for _, h := range []string{AuthTenantHeader, TenantHeader} {
		r := withSession(httptest.NewRequest("GET", "/x", nil))
		r.Header.Set(h, other.String())
		if code, _ := run(r); code != http.StatusForbidden {
			t.Errorf("%s naming another tenant: got %d, want 403", h, code)
		}
	}
	r := withSession(httptest.NewRequest("GET", "/x", nil))
	r.Header.Set(TenantHeader, "not-a-uuid")
	if code, _ := run(r); code != http.StatusForbidden {
		t.Errorf("malformed tenant header with a session: got %d, want 403", code)
	}
	r = withSession(httptest.NewRequest("GET", "/x", nil))
	r.Header.Set(AuthTenantHeader, sessTenant.String())
	if code, got := run(r); code != http.StatusOK || got != sessTenant {
		t.Errorf("matching header: got %d tenant %v, want 200 %v", code, got, sessTenant)
	}
	if code, got := run(withSession(httptest.NewRequest("GET", "/x", nil))); code != http.StatusOK || got != sessTenant {
		t.Errorf("session without header: got %d tenant %v, want 200 %v", code, got, sessTenant)
	}

	// Cookieless + forged header + no internal key → 401.
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(TenantHeader, other.String())
	r.Header.Set("X-User-ID", uuid.NewString())
	r.Header.Set("X-User-Role", "admin")
	if code, _ := run(r); code != http.StatusUnauthorized {
		t.Errorf("forged tenant header without session or key: got %d, want 401", code)
	}
	// Internal caller with the key → header tenant honoured.
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(TenantHeader, other.String())
	r.Header.Set(InternalServiceKeyHeader, testInternalKey)
	if code, got := run(r); code != http.StatusOK || got != other {
		t.Errorf("internal caller: got %d tenant %v, want 200 %v", code, got, other)
	}
}

// TestTenantInterceptor_ContextMismatchDenied: the gRPC twin — a tenant
// already on the context wins and metadata naming another tenant is
// PermissionDenied.
func TestTenantInterceptor_ContextMismatchDenied(t *testing.T) {
	ctxTenant, other := uuid.New(), uuid.New()
	ctx := auth.SetTenantID(context.Background(), ctxTenant)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(TenantMetadataKey, other.String()))
	_, err := TenantInterceptor(nil)(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		t.Fatal("handler must not run")
		return nil, nil
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("got %v, want PermissionDenied", err)
	}

	ctx = metadata.NewIncomingContext(auth.SetTenantID(context.Background(), ctxTenant),
		metadata.Pairs(TenantMetadataKey, ctxTenant.String()))
	var seen uuid.UUID
	_, err = TenantInterceptor(nil)(ctx, nil, &grpc.UnaryServerInfo{}, func(c context.Context, _ any) (any, error) {
		seen, _ = auth.GetTenantID(c)
		return nil, nil
	})
	if err != nil || seen != ctxTenant {
		t.Fatalf("matching metadata: err=%v tenant=%v", err, seen)
	}
}
