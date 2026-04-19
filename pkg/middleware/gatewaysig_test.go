package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func newReq(path, header string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if header != "" {
		r.Header.Set(GatewaySignatureHeader, header)
	}
	return r
}

func TestRequireGatewaySignature_AcceptsValidHeader(t *testing.T) {
	h := RequireGatewaySignatureWithSecrets([]string{"shhh"})(http.HandlerFunc(ok))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq("/api/v1/documents", "shhh"))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRequireGatewaySignature_RejectsMissingHeader(t *testing.T) {
	h := RequireGatewaySignatureWithSecrets([]string{"shhh"})(http.HandlerFunc(ok))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq("/api/v1/documents", ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "missing") {
		t.Errorf("error body should say missing: %q", rr.Body.String())
	}
}

func TestRequireGatewaySignature_RejectsWrongHeader(t *testing.T) {
	h := RequireGatewaySignatureWithSecrets([]string{"shhh"})(http.HandlerFunc(ok))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq("/api/v1/documents", "bad-guess"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid") {
		t.Errorf("error body should say invalid: %q", rr.Body.String())
	}
}

// Regression pin: rotation means accepting BOTH old and new secret for
// the overlap window. Dropping this breaks the runbook.
func TestRequireGatewaySignature_AcceptsAnyOfMultipleSecrets(t *testing.T) {
	h := RequireGatewaySignatureWithSecrets([]string{"old", "new"})(http.HandlerFunc(ok))
	for _, s := range []string{"old", "new"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, newReq("/", s))
		if rr.Code != http.StatusOK {
			t.Errorf("secret %q rejected with %d", s, rr.Code)
		}
	}
}

// Health endpoints must NOT require the header — k8s probes hit them
// directly on a pod IP without going through the gateway.
func TestRequireGatewaySignature_AllowsHealthEndpointsUnsigned(t *testing.T) {
	h := RequireGatewaySignatureWithSecrets([]string{"shhh"})(http.HandlerFunc(ok))
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, newReq(p, ""))
		if rr.Code != http.StatusOK {
			t.Errorf("probe path %q blocked (code %d) — k8s probes would fail", p, rr.Code)
		}
	}
}
