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

// Rotation flow: setting GatewaySignaturePrevEnv alongside the active
// secret should accept BOTH headers via the env-driven constructor.
// Empty / unequal handling: empty PREV is ignored, equal-to-current is
// deduped (we don't double-register the same secret).
func TestRequireGatewaySignature_HonorsPrevEnvForRotation(t *testing.T) {
	t.Setenv(GatewaySignatureEnv, "new-secret")
	t.Setenv(GatewaySignaturePrevEnv, "old-secret")
	h := RequireGatewaySignature()(http.HandlerFunc(ok))
	for _, s := range []string{"old-secret", "new-secret"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, newReq("/api/v1/documents", s))
		if rr.Code != http.StatusOK {
			t.Errorf("secret %q rejected with %d during rotation overlap", s, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq("/api/v1/documents", "neither"))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("non-matching secret should still be rejected during rotation: got %d", rr.Code)
	}
}

func TestRequireGatewaySignature_PrevEnvEmptyIsIgnored(t *testing.T) {
	t.Setenv(GatewaySignatureEnv, "only-one")
	t.Setenv(GatewaySignaturePrevEnv, "")
	h := RequireGatewaySignature()(http.HandlerFunc(ok))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, newReq("/", "only-one"))
	if rr.Code != http.StatusOK {
		t.Fatalf("active secret rejected: %d", rr.Code)
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
