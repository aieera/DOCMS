package handler

import "net/http"

// MaxJSONBody is the DEC-19 request-body cap for JSON routes (1 MiB).
//
// DEC-19 defines middleware.MaxBody / MaxJSONBody in pkg/middleware
// (WS-PKG). Until that lands on this branch, the DOC3-owned routes use this
// package-local equivalent; swap limitBody for middleware.MaxBody when
// merging.
const MaxJSONBody int64 = 1 << 20

// limitBody caps the request body at n bytes: a declared Content-Length
// above n is 413 before the handler runs, and the body is wrapped in
// http.MaxBytesReader so an undeclared (chunked) oversize body fails the
// handler's read.
func limitBody(n int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}

// limitJSON is limitBody(MaxJSONBody) for a HandlerFunc.
func limitJSON(fn http.HandlerFunc) http.Handler { return limitBody(MaxJSONBody, fn) }
