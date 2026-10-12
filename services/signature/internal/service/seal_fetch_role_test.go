package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchVersionPDF_SendsExplicitRole (C-6 / DEC-3): the seal pipeline
// reads the version through the document service's SessionOrAPIKey
// internal-service path, which now REQUIRES X-User-Role (a missing role is
// 401; it used to default to admin). The seal acts for the tenant as the
// system, so it names the admin role explicitly.
func TestFetchVersionPDF_SendsExplicitRole(t *testing.T) {
	var got http.Header
	docSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("%PDF-1.7"))
	}))
	t.Cleanup(docSrv.Close)

	sl := &Sealer{docHTTPBase: docSrv.URL, httpClient: docSrv.Client(), internalKey: "k"}
	if _, err := sl.fetchVersionPDF(context.Background(),
		"11111111-1111-1111-1111-111111111111", "doc-1", "ver-1"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.Get(internalServiceKeyHeader) != "k" {
		t.Fatalf("internal key header = %q", got.Get(internalServiceKeyHeader))
	}
	if role := got.Get("X-User-Role"); role != "admin" {
		t.Fatalf("X-User-Role = %q; want an explicit \"admin\" (the receiver no longer defaults a missing role)", role)
	}
}
