package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// DEC-19: JSON routes take at most 1 MiB, the capture routes (document
// bytes) 64 MiB; anything declared larger is refused with 413 before the
// handler runs.
func TestConnectorAPI_413OverLimit(t *testing.T) {
	reached := 0
	h := withBodyLimits(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	}))
	send := func(path string, size int, declared int64) int {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(make([]byte, size)))
		if declared > 0 {
			req.ContentLength = declared
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	for _, p := range []string{"/api/v1/webhooks", "/api/v1/admin/email-configs", "/api/v1/admin/intake/folders", "/api/v1/connectors/google/config"} {
		if code := send(p, 2<<20, 0); code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with 2 MiB: got %d, want 413", p, code)
		}
	}
	if reached != 0 {
		t.Fatalf("over-limit JSON bodies reached the handler %d times", reached)
	}
	if code := send("/api/v1/connectors/capture/commit", 2<<20, 0); code != http.StatusNoContent {
		t.Errorf("capture with 2 MiB: got %d, want it accepted", code)
	}
	if code := send("/api/v1/connectors/capture/analyze", 16, 65<<20); code != http.StatusRequestEntityTooLarge {
		t.Errorf("capture declaring 65 MiB: got %d, want 413", code)
	}
}

func TestConnectorAPI_ServerTimeouts(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 60*time.Second ||
		srv.WriteTimeout != 120*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("timeouts = header %v read %v write %v idle %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}
