package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/middleware"
)

// DEC-19: every document route refuses a body over its limit with 413
// before the handler acts on it — 1 MiB for JSON/API routes, 64 MiB for
// routes that carry document bytes — and the server has read/write/idle
// timeouts.
func TestDocumentAPI_413OverLimit(t *testing.T) {
	var read int64
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		read = n
		if middleware.BodyTooLarge(err) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := withBodyLimits(sink)
	send := func(method, path string, size int64, chunked bool) int {
		t.Helper()
		var body io.Reader = bytes.NewReader(make([]byte, size))
		if chunked {
			body = io.LimitReader(zeroReader{}, size) // unknown length
		}
		req := httptest.NewRequest(method, path, body)
		if chunked {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		read = 0
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	over1, over64 := middleware.MaxJSONBody+1, middleware.MaxDocumentBody+1

	// JSON / API routes: 1 MiB, declared or streamed.
	for _, p := range []string{
		"/api/v1/documents",
		"/api/v1/records/schedules",
		"/api/v1/storage/uploads/initiate",
		"/api/v1/contracts/" + "d" + "/edges",
		"/internal/v1/records/cutoff-sweep",
		"/api/v1/workspaces/w/documents:upsert",
	} {
		require.Equal(t, http.StatusRequestEntityTooLarge, send(http.MethodPost, p, over1, false), p)
		require.Zero(t, read, "%s: refused before the handler reads", p)
		require.Equal(t, http.StatusRequestEntityTooLarge, send(http.MethodPost, p, over1, true), "%s chunked", p)
		require.Equal(t, http.StatusNoContent, send(http.MethodPost, p, 64<<10, false), p)
	}

	// Document-carrying routes: 64 MiB.
	for _, p := range []string{
		"/wopi/files/f1/contents",
		"/api/v1/integrations/m365/ingest-email",
		"/api/v1/admin/bulk/import",
	} {
		require.Equal(t, http.StatusNoContent, send(http.MethodPost, p, over1, false), p)
		require.Equal(t, http.StatusRequestEntityTooLarge, send(http.MethodPost, p, over64, false), p)
		require.Equal(t, http.StatusRequestEntityTooLarge, send(http.MethodPost, p, over64, true), "%s chunked", p)
	}

	// Self-limited mounts are passed through to their own limit.
	require.Equal(t, http.StatusNoContent, send(http.MethodPost, "/api/v1/irm/export", over1, false))
	// A streaming download is still body-limited (it has none to speak of).
	require.Equal(t, http.StatusRequestEntityTooLarge,
		send(http.MethodGet, "/api/v1/shared/tok/download", over1, false))

	srv := newHTTPServer(":0", h)
	require.Equal(t, 60*time.Second, srv.ReadTimeout)
	require.Equal(t, 120*time.Second, srv.WriteTimeout)
	require.Equal(t, 120*time.Second, srv.IdleTimeout)
	require.NotZero(t, srv.ReadHeaderTimeout)
	require.True(t, strings.HasPrefix(srv.Addr, ":"))
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
