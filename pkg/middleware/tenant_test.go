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

func TestTenantHTTP_PutsTenantOnContext(t *testing.T) {
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
	w := httptest.NewRecorder()
	TenantHTTP(nil)(next).ServeHTTP(w, req)

	if seen != want {
		t.Errorf("downstream ctx tenant: got %v, want %v", seen, want)
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
