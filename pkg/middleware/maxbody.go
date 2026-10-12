package middleware

import (
	"errors"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
)

// Body-size limits for MaxBody (DEC-19): JSON/API routes take 1 MiB,
// routes that carry document bytes take 64 MiB.
const (
	MaxJSONBody     int64 = 1 << 20
	MaxDocumentBody int64 = 64 << 20
)

// MaxBody caps the request body at n bytes (DEC-19). A request whose
// declared Content-Length exceeds n is refused with 413 before the handler
// runs; otherwise the body is wrapped in http.MaxBytesReader, so a body
// that only turns out too large while streaming (chunked) fails the
// handler's read with *http.MaxBytesError — see BodyTooLarge — and the
// server closes the connection instead of draining the rest.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
					"type":           "PAYLOAD_TOO_LARGE",
					"message":        "request body too large",
					"correlation_id": auth.GetCorrelationID(r.Context()),
				})
				return
			}
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyTooLarge reports whether err (e.g. from json.Decoder.Decode or
// io.ReadAll on a MaxBody-wrapped request) means the body exceeded the
// limit, so handlers can answer 413 rather than a generic 400.
func BodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
