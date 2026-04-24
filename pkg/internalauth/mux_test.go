package internalauth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fallbackGateway(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Gateway-Signature") != secret {
				http.Error(w, "bad gateway signature", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TestMux_NilVerifier_FallsBackOnNonProbePaths(t *testing.T) {
	h := Mux(okHandler(), nil, fallbackGateway("kong"))

	// /api/ with bad gateway sig → rejected
	r1 := httptest.NewRequest("GET", "/api/v1/docs", nil)
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without gateway sig, got %d", w1.Code)
	}

	// /api/ with good sig → 200
	r2 := httptest.NewRequest("GET", "/api/v1/docs", nil)
	r2.Header.Set("X-Gateway-Signature", "kong")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w2.Code)
	}

	// probe bypasses.
	r3 := httptest.NewRequest("GET", "/healthz", nil)
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, r3)
	if w3.Code != http.StatusOK {
		t.Fatalf("want 200 for probe, got %d", w3.Code)
	}
}

func TestMux_VerifierGuardsInternal_GatewayGuardsOthers(t *testing.T) {
	v, err := New(Config{
		Mode:          ModeHMAC,
		HMACSecret:    "s3cret",
		ClockSkewSecs: 300,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := Mux(okHandler(), v, fallbackGateway("kong"))

	// /internal with gateway sig but no internal sig → 401.
	r1 := httptest.NewRequest("POST", "/internal/v1/sweep", nil)
	r1.Header.Set("X-Gateway-Signature", "kong")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("/internal with only gateway sig must be 401, got %d", w1.Code)
	}

	// /internal with valid internal HMAC → 200.
	body := []byte(`{}`)
	r2 := httptest.NewRequest("POST", "/internal/v1/sweep", bytes.NewReader(body))
	r2.Header.Set(InternalSignatureHeader, SignRequest("s3cret", "POST", "/internal/v1/sweep", body, time.Now().Unix()))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("/internal with valid HMAC must be 200, got %d (%s)", w2.Code, w2.Body.String())
	}

	// /api with valid gateway sig → 200 (no internal sig needed).
	r3 := httptest.NewRequest("GET", "/api/v1/docs", nil)
	r3.Header.Set("X-Gateway-Signature", "kong")
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, r3)
	if w3.Code != http.StatusOK {
		t.Fatalf("/api with gateway sig must be 200, got %d", w3.Code)
	}
}
