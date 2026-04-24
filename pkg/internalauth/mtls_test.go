package internalauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func verifierWithCA(t *testing.T, ca *testCA, sans []string) *Verifier {
	t.Helper()
	v, err := New(Config{
		Mode:         ModeMTLS,
		CAPool:       ca.Pool(),
		SANAllowlist: sans,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestMTLS_ValidCertPasses(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v := verifierWithCA(t, ca, []string{"worker.temporal.internal"})

	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r.TLS = fakeTLSState(leaf)
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestMTLS_WrongCARejected(t *testing.T) {
	goodCA := newTestCA(t)
	attackerCA := newTestCA(t)
	attackerLeaf := attackerCA.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v := verifierWithCA(t, goodCA, []string{"worker.temporal.internal"})

	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r.TLS = fakeTLSState(attackerLeaf)
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestMTLS_ExpiredCertRejected(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	v := verifierWithCA(t, ca, []string{"worker.temporal.internal"})

	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r.TLS = fakeTLSState(leaf)
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestMTLS_SANNotInAllowlistRejected(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issue(t, []string{"unknown.peer.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v := verifierWithCA(t, ca, []string{"worker.temporal.internal"})

	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r.TLS = fakeTLSState(leaf)
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestMTLS_EmptyAllowlistFailsClosed(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issue(t, []string{"worker.temporal.internal"},
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	v := verifierWithCA(t, ca, nil) // no SANs configured

	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	r.TLS = fakeTLSState(leaf)
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty allowlist must reject; got %d", w.Code)
	}
}

func TestMTLS_NoPeerCertRejected(t *testing.T) {
	ca := newTestCA(t)
	v := verifierWithCA(t, ca, []string{"worker.temporal.internal"})
	r := httptest.NewRequest("POST", "/internal/v1/x", nil) // no r.TLS
	w := httptest.NewRecorder()
	v.RequireInternalMTLS(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 when no peer cert, got %d", w.Code)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
