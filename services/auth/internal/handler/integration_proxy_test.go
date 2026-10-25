package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
)

// The ERP proxy stamps X-ERP-User / X-ERP-Admin server-side. Under the old
// ReverseProxy.Director, hop-by-hop headers were stripped AFTER the Director
// ran, so a client could name the stamped headers in "Connection" and have
// the proxy delete them. Rewrite strips hop-by-hop headers first; this test
// pins that the stamped identity, the path rewrite and the credential strip
// all survive such a request.
func TestIntegrationERPProxy_StampedHeadersSurviveConnectionHeader(t *testing.T) {
	var got *http.Request
	bff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer bff.Close()

	h := New(Config{IntegrationBFFURL: bff.URL})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/erp/customers/42?x=1", nil)
	req.Header.Set("Connection", "X-ERP-User, X-ERP-Admin")
	req.Header.Set("X-ERP-User", "attacker@example.com")
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	req.Header.Set("Cookie", "dms_session=secret")
	req.Header.Set("X-Csrf-Token", "tok")
	req = req.WithContext(auth.WithUser(req.Context(), auth.UserInfo{
		ID:    uuid.New(),
		Email: "admin@tenant.example",
		Role:  "admin",
	}))

	rec := httptest.NewRecorder()
	h.IntegrationERPProxy(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %q)", rec.Code, rec.Body.String())
	}
	if got == nil {
		t.Fatal("BFF never received the request")
	}
	if got.URL.Path != "/files/customers/42" || got.URL.RawQuery != "x=1" {
		t.Errorf("upstream URL = %s?%s, want /files/customers/42?x=1", got.URL.Path, got.URL.RawQuery)
	}
	if v := got.Header.Get("X-ERP-User"); v != "admin@tenant.example" {
		t.Errorf("X-ERP-User = %q, want the session user", v)
	}
	if v := got.Header.Get("X-ERP-Admin"); v != "true" {
		t.Errorf("X-ERP-Admin = %q, want true", v)
	}
	for _, hdr := range []string{"Cookie", "X-Csrf-Token", "X-Gateway-Signature"} {
		if v := got.Header.Get(hdr); v != "" {
			t.Errorf("%s leaked to the BFF: %q", hdr, v)
		}
	}
	if v := got.Header.Get("X-Forwarded-For"); v == "" || strings.Contains(v, "6.6.6.6") {
		t.Errorf("X-Forwarded-For = %q, want only the TCP peer", v)
	}
}
