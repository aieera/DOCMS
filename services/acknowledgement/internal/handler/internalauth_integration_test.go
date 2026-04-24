//go:build integration

// Integration test for the /internal/v1/acknowledgement/sweep-reminders
// auth chain. Exercises the internalauth.Mux wiring against the real
// handler router without a DB round-trip — a stub service captures
// invocations so we can assert the request ever reached the handler.
//
// ADR 0031 / T-D-1: holding VAULTDMS_GATEWAY_SECRET must NOT be
// sufficient to POST to /internal/*; only callers with a valid
// internal HMAC (or mTLS cert — not exercised here) get through.

package handler

import (
	"bytes"
	"crypto/hmac"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vaultdms/vaultdms/pkg/internalauth"
)

// fakeGatewaySig mimics pkg/middleware.RequireGatewaySignature in the
// constrained scope this test needs, without booting the shared-secret
// env-var dance. It accepts any non-empty X-Gateway-Signature.
func fakeGatewaySig() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !hmac.Equal([]byte(r.Header.Get("X-Gateway-Signature")), []byte("kong-dev")) {
				http.Error(w, "bad gateway sig", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sweepStub is a chi router that registers the same path as the real
// handler but records invocations in `called` instead of hitting the
// service. Keeps this test hermetic (no Postgres / NATS containers
// needed) while still exercising the real internalauth.Mux routing
// + HMAC validation against the real handler path.
type sweepStub struct {
	called int
}

func (s *sweepStub) Register(r *chi.Mux) {
	r.Post("/internal/v1/acknowledgement/sweep-reminders", func(w http.ResponseWriter, _ *http.Request) {
		s.called++
		w.WriteHeader(http.StatusOK)
	})
}

func buildServer(t *testing.T, secret string) (*httptest.Server, *sweepStub) {
	t.Helper()
	stub := &sweepStub{}
	r := chi.NewRouter()
	stub.Register(r)

	v, err := internalauth.New(internalauth.Config{
		Mode:          internalauth.ModeHMAC,
		HMACSecret:    secret,
		ClockSkewSecs: 300,
	})
	if err != nil {
		t.Fatalf("internalauth.New: %v", err)
	}
	h := internalauth.Mux(r, v, fakeGatewaySig())
	return httptest.NewServer(h), stub
}

func TestIntegration_SweepReminders_ValidInternalHMAC_200(t *testing.T) {
	srv, stub := buildServer(t, "internal-dev-secret")
	defer srv.Close()

	body := []byte(`{"tenant_id":"acme"}`)
	ts := time.Now().Unix()
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/internal/v1/acknowledgement/sweep-reminders",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(internalauth.InternalSignatureHeader,
		internalauth.SignRequest("internal-dev-secret", http.MethodPost,
			"/internal/v1/acknowledgement/sweep-reminders", body, ts))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 with valid internal HMAC, got %d", resp.StatusCode)
	}
	if stub.called != 1 {
		t.Fatalf("handler should have fired exactly once, got %d", stub.called)
	}
}

func TestIntegration_SweepReminders_OnlyGatewaySig_401(t *testing.T) {
	// This is the T-D-1 regression guard: gateway signature alone
	// was previously sufficient on /internal/*. Now it is not.
	srv, stub := buildServer(t, "internal-dev-secret")
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/internal/v1/acknowledgement/sweep-reminders",
		bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Only the outer gateway signature — NO internal signature.
	req.Header.Set("X-Gateway-Signature", "kong-dev")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 when only gateway sig is presented, got %d", resp.StatusCode)
	}
	if stub.called != 0 {
		t.Fatal("handler must not have fired for an unauthenticated internal request")
	}
}

func TestIntegration_SweepReminders_StaleTimestamp_401(t *testing.T) {
	// Replay / skew guard: a captured signature replayed 10 minutes
	// later should not be accepted.
	srv, stub := buildServer(t, "internal-dev-secret")
	defer srv.Close()

	body := []byte(`{}`)
	staleTS := time.Now().Add(-10 * time.Minute).Unix()
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/internal/v1/acknowledgement/sweep-reminders",
		bytes.NewReader(body))
	req.Header.Set(internalauth.InternalSignatureHeader,
		internalauth.SignRequest("internal-dev-secret", http.MethodPost,
			"/internal/v1/acknowledgement/sweep-reminders", body, staleTS))

	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for 10-minute-stale signature, got %d", resp.StatusCode)
	}
	if stub.called != 0 {
		t.Fatal("handler must not have fired for a stale signature")
	}
}
