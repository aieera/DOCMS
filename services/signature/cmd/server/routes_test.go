package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/signature/internal/handler"
)

const testGatewaySecret = "test-gateway-secret-0123456789abcdef"

// testRouter builds the production router with no database and no service:
// every request in these tests must be decided by the middleware before a
// handler (which would dereference the nil service) runs.
func testRouter(t *testing.T) http.Handler {
	t.Helper()
	t.Setenv("SEDOC_GATEWAY_SECRET", testGatewaySecret)
	t.Setenv("SEDOC_INTERNAL_API_KEY", "internal-key-for-tests")
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

// C-6 / H-3: a request that carries a valid gateway signature (as the Vite
// host-mode proxy adds) and forged identity headers but no session cookie
// must be rejected with 401 on every user-facing route — including the
// anonymous-before verify-bytes endpoint.
func TestSignatureAPI_NoSession401WithForgedHeaders(t *testing.T) {
	router := testRouter(t)
	tenant, user := uuid.NewString(), uuid.NewString()
	reqID, signerID, docID := uuid.NewString(), uuid.NewString(), uuid.NewString()

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/signatures/requests"},
		{http.MethodGet, "/api/v1/signatures/requests/" + reqID},
		{http.MethodGet, "/api/v1/signatures/document/" + docID},
		{http.MethodPost, fmt.Sprintf("/api/v1/signatures/requests/%s/sign/%s?token=x", reqID, signerID)},
		{http.MethodPost, "/api/v1/signatures/requests/" + reqID + "/cancel"},
		{http.MethodGet, "/api/v1/signatures/verify/" + docID},
		{http.MethodPost, "/api/v1/signatures/verify-bytes"},
		{http.MethodPost, "/api/v1/signatures/requests/" + reqID + "/in-person/sign"},
		{http.MethodPost, "/api/v1/signatures/qes/start"},
		{http.MethodGet, "/api/v1/signatures/qes/return?session=s&state=s&code=c"},
		{http.MethodGet, "/api/v1/signatures/qes/session/" + reqID},
		{http.MethodGet, "/api/v1/signatures/qes/certificates?request_id=" + reqID},
		{http.MethodPost, "/api/v1/signatures/esign/send"},
		{http.MethodGet, "/api/v1/signatures/esign/connections"},
		{http.MethodPost, "/api/v1/signatures/esign/oauth/start"},
		{http.MethodGet, "/api/v1/signatures/esign/oauth/callback?code=c&state=" + tenant + ".docusign.x"},
		{http.MethodPost, "/api/v1/signatures/esign/disconnect"},
		{http.MethodPost, "/api/v1/signatures/esign/connections/docusign/refresh"},
		{http.MethodGet, "/api/v1/signatures/esign/envelopes"},
		{http.MethodGet, "/api/v1/signatures/esign/provider-config/docusign"},
		{http.MethodPut, "/api/v1/signatures/esign/provider-config/docusign"},
		{http.MethodDelete, "/api/v1/signatures/esign/provider-config/docusign"},
		{http.MethodPost, "/api/v1/signatures/internal/seal"},
		{http.MethodPost, "/api/v1/signatures/internal/seal-ceremony"},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Gateway-Signature", testGatewaySecret)
			r.Header.Set("X-Tenant-ID", tenant)
			r.Header.Set("X-Auth-Tenant-ID", tenant)
			r.Header.Set("X-User-ID", user)
			r.Header.Set("X-User-Role", "admin")
			if got := serve(router, r); got != http.StatusUnauthorized {
				t.Fatalf("%s %s without a session: got %d, want 401", rt.method, rt.path, got)
			}
		})
	}
}

// repeatReader yields n copies of b without allocating them.
type repeatReader struct {
	b byte
	n int64
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.n {
		p = p[:r.n]
	}
	for i := range p {
		p[i] = r.b
	}
	r.n -= int64(len(p))
	return len(p), nil
}

// S-5 / R-5: the vendor webhook is anonymous at the transport (no session,
// no gateway signature) and used to read an unbounded body before checking
// the HMAC. Anything over 64 MiB must be refused with 413 before the
// service is reached.
func TestEsignWebhook_413OverLimit(t *testing.T) {
	router := testRouter(t)
	body := io.MultiReader(strings.NewReader(`{"envelopeId":"`), &repeatReader{b: 'a', n: 64<<20 + 1})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/signatures/esign/webhook/docusign/"+uuid.NewString(), body)
	if got := serve(router, r); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized webhook body: got %d, want 413", got)
	}
}
