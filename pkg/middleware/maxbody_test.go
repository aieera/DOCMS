package middleware

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMaxBody_413OverLimit pins DEC-19: a declared Content-Length over the
// limit is refused with 413 before the handler runs; a body that only
// turns out too large while streaming (chunked, no Content-Length) makes
// the handler's read fail with *http.MaxBytesError, which BodyTooLarge
// recognizes so the handler can answer 413 too; bodies within the limit
// pass through intact.
func TestMaxBody_413OverLimit(t *testing.T) {
	const limit = 16
	var handlerRan bool
	var readErr error
	var readBody string
	h := MaxBody(limit)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		b, err := io.ReadAll(r.Body)
		readErr, readBody = err, string(b)
		if BodyTooLarge(err) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("declared length over limit", func(t *testing.T) {
		handlerRan = false
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", limit+1))))
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status: got %d, want 413", rr.Code)
		}
		if handlerRan {
			t.Fatal("handler must not run for a declared oversize body")
		}
	})

	t.Run("streamed body over limit", func(t *testing.T) {
		handlerRan = false
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", io.NopCloser(strings.NewReader(strings.Repeat("a", limit*4))))
		req.ContentLength = -1 // chunked: size unknown up front
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if !handlerRan {
			t.Fatal("handler should run when the size is not declared")
		}
		var mbe *http.MaxBytesError
		if !errors.As(readErr, &mbe) {
			t.Fatalf("read error: got %v, want *http.MaxBytesError", readErr)
		}
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status: got %d, want 413", rr.Code)
		}
	})

	t.Run("within limit", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", strings.NewReader("hello")))
		if rr.Code != http.StatusOK || readBody != "hello" || readErr != nil {
			t.Fatalf("got status %d body %q err %v", rr.Code, readBody, readErr)
		}
	})
}
