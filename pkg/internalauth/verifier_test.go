package internalauth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMode_BothPrefersMTLS_FallsBackToHMAC(t *testing.T) {
	ca := newTestCA(t)
	goodLeaf := ca.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v, err := New(Config{
		Mode:         ModeBoth,
		CAPool:       ca.Pool(),
		SANAllowlist: []string{"worker.temporal.internal"},
		HMACSecret:   "s3cret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// With peer cert → mTLS path taken and succeeds.
	r1 := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r1.TLS = fakeTLSState(goodLeaf)
	w1 := httptest.NewRecorder()
	v.RequireInternal(okHandler()).ServeHTTP(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("mTLS path: want 200, got %d", w1.Code)
	}

	// Without peer cert → HMAC path.
	body := []byte(`{}`)
	r2 := httptest.NewRequest("POST", "/internal/v1/x", bytes.NewReader(body))
	r2.Header.Set(InternalSignatureHeader, SignRequest("s3cret", "POST", "/internal/v1/x", body, time.Now().Unix()))
	w2 := httptest.NewRecorder()
	v.RequireInternal(okHandler()).ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("HMAC fallback: want 200, got %d (%s)", w2.Code, w2.Body.String())
	}
}

func TestMode_BothFailsClosedOnBadMTLS(t *testing.T) {
	// A cert presented but untrusted must NOT silently fall through
	// to HMAC — that's an allowlist bypass if the attacker has the
	// HMAC secret AND presents a cert to make themselves look like a
	// legit peer.
	goodCA := newTestCA(t)
	attackerCA := newTestCA(t)
	badLeaf := attackerCA.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v, err := New(Config{
		Mode:         ModeBoth,
		CAPool:       goodCA.Pool(),
		SANAllowlist: []string{"worker.temporal.internal"},
		HMACSecret:   "s3cret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Present a bad cert AND a valid HMAC. Must still reject.
	body := []byte(`{}`)
	r := httptest.NewRequest("POST", "/internal/v1/x", bytes.NewReader(body))
	r.TLS = fakeTLSState(badLeaf)
	r.Header.Set(InternalSignatureHeader, SignRequest("s3cret", "POST", "/internal/v1/x", body, time.Now().Unix()))
	w := httptest.NewRecorder()
	v.RequireInternal(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad cert with good HMAC must fail closed, got %d", w.Code)
	}
}

func TestMode_HealthEndpointsSkipAuth(t *testing.T) {
	v, err := New(Config{Mode: ModeHMAC, HMACSecret: "s3cret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		r := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		v.RequireInternal(okHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s must bypass auth, got %d", p, w.Code)
		}
	}
}

func TestNew_RejectsInconsistentConfig(t *testing.T) {
	// mTLS mode with no CA pool.
	if _, err := New(Config{Mode: ModeMTLS}); err == nil {
		t.Fatal("want error: mtls without CAPool")
	}
	// HMAC mode with no secret.
	if _, err := New(Config{Mode: ModeHMAC}); err == nil {
		t.Fatal("want error: hmac without secret")
	}
	// Unknown mode.
	if _, err := New(Config{Mode: "nope"}); err == nil {
		t.Fatal("want error: unknown mode")
	}
}
