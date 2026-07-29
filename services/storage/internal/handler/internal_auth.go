package handler

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// internalServiceKeyOK constant-time-compares the caller's X-Internal-Service-Key
// against SEDOC_INTERNAL_API_KEY and fails closed when the env key is unset.
//
// The compare is constant-time: a byte-by-byte `!=` on the shared internal key
// leaks, via response timing, how many leading bytes matched — enough to recover
// the key one byte at a time. These endpoints (WORM lock, DEK rewrap, re-encrypt)
// are privileged, so the key must not be timing-recoverable.
func internalServiceKeyOK(r *http.Request) bool {
	want := os.Getenv("SEDOC_INTERNAL_API_KEY")
	if want == "" {
		return false
	}
	got := r.Header.Get("X-Internal-Service-Key")
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
