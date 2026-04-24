package internalauth

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func hmacVerifier(t *testing.T) *Verifier {
	t.Helper()
	v, err := New(Config{
		Mode:          ModeHMAC,
		HMACSecret:    "s3cret",
		ClockSkewSecs: 300,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func signedRequest(method, path string, body []byte, ts int64, secret string) *http.Request {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set(InternalSignatureHeader, SignRequest(secret, method, path, body, ts))
	return r
}

func TestHMAC_ValidSignaturePasses(t *testing.T) {
	v := hmacVerifier(t)
	body := []byte(`{"tenant_id":"acme"}`)
	r := signedRequest("POST", "/internal/v1/acknowledgement/sweep-reminders", body, time.Now().Unix(), "s3cret")
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(echoBodyHandler(t, body)).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHMAC_SkewBeyondWindowRejected(t *testing.T) {
	v := hmacVerifier(t)
	body := []byte(`{}`)
	// 6 minutes in the past → outside the 5-minute window.
	r := signedRequest("POST", "/internal/v1/x", body, time.Now().Add(-6*time.Minute).Unix(), "s3cret")
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for stale timestamp, got %d", w.Code)
	}
}

func TestHMAC_WrongSecretRejected(t *testing.T) {
	v := hmacVerifier(t)
	body := []byte(`{}`)
	r := signedRequest("POST", "/internal/v1/x", body, time.Now().Unix(), "wrong-secret")
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for bad signature, got %d", w.Code)
	}
}

func TestHMAC_TamperedBodyRejected(t *testing.T) {
	v := hmacVerifier(t)
	body := []byte(`{"tenant_id":"acme"}`)
	ts := time.Now().Unix()
	r := httptest.NewRequest("POST", "/internal/v1/x", bytes.NewReader([]byte(`{"tenant_id":"evil"}`)))
	r.Header.Set(InternalSignatureHeader, SignRequest("s3cret", "POST", "/internal/v1/x", body, ts))
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 when body changed, got %d", w.Code)
	}
}

func TestHMAC_MissingHeaderRejected(t *testing.T) {
	v := hmacVerifier(t)
	r := httptest.NewRequest("POST", "/internal/v1/x", nil)
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(okHandler()).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 when header missing, got %d", w.Code)
	}
}

func TestHMAC_BodyRestoredForHandler(t *testing.T) {
	// Verifier must leave r.Body readable by downstream handlers.
	v := hmacVerifier(t)
	body := []byte(`{"hello":"world"}`)
	r := signedRequest("POST", "/internal/v1/x", body, time.Now().Unix(), "s3cret")
	w := httptest.NewRecorder()
	v.RequireInternalHMAC(echoBodyHandler(t, body)).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("downstream handler failed: %d %s", w.Code, w.Body.String())
	}
}

func echoBodyHandler(t *testing.T, want []byte) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
			return
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("body mismatch: got %q want %q", got, want)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
