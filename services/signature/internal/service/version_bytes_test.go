package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The fetcher must read the version as the calling user (so the document
// service's view check applies to them) and never fall back to the
// internal-service default role.
func TestDocumentFetcher_ActsAsCaller(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path != "/api/v1/documents/doc-1/versions/ver-1/decrypt-stream" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-User-Role") != "member" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("%PDF bytes"))
	}))
	defer srv.Close()

	svc := &Service{}
	svc.AddDocumentFetcher(srv.URL, "ikey", "gsig")
	b, err := svc.versionBytes(context.Background(),
		Actor{TenantID: "t-1", UserID: "u-1", Role: "member"}, "doc-1", "ver-1")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(b) != "%PDF bytes" {
		t.Fatalf("bytes = %q", b)
	}
	for k, want := range map[string]string{
		"X-Internal-Service-Key": "ikey", "X-Gateway-Signature": "gsig",
		"X-Auth-Tenant-Id": "t-1", "X-User-Id": "u-1", "X-User-Role": "member",
	} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, got.Get(k), want)
		}
	}

	if _, err := svc.versionBytes(context.Background(),
		Actor{TenantID: "t-1", UserID: "u-1"}, "doc-1", "ver-1"); err == nil {
		t.Fatal("a fetch without a role must be refused (the internal path would default it to admin)")
	}
	_, err = svc.versionBytes(context.Background(),
		Actor{TenantID: "t-1", UserID: "u-1", Role: "viewer"}, "doc-1", "ver-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("document 403 must surface as ErrForbidden, got %v", err)
	}
}
