package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// DEC-19: every JSON API caps request bodies at 1 MiB (middleware.MaxBody)
// ahead of authentication, so an oversized body is refused with 413
// before any session lookup or decode.
func TestAPI_413OverLimit(t *testing.T) {
	t.Setenv("SEDOC_GATEWAY_SECRET", "gw-413")
	reached := false
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	body := bytes.Repeat([]byte("a"), 2<<20)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/audit/data-subject/anonymize", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Gateway-Signature", "gw-413")
	w := httptest.NewRecorder()
	newHTTPHandler(nil, stub).ServeHTTP(w, r)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.False(t, reached)
}
