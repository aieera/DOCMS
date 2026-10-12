package handler

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Request-body limits (DEC-19; review findings S-5, R-5). JSON routes take
// at most 1 MiB; routes that may carry a document (QES start and e-sign send
// from pre-DEC-17 clients, verify-bytes, the vendor webhook) take 64 MiB.
//
// middleware.MaxBody is being added to pkg/middleware by another
// workstream; until it lands, limitBody is the same http.MaxBytesReader
// wrapper kept local to this service.
const (
	jsonBodyLimit     int64 = 1 << 20
	documentBodyLimit int64 = 64 << 20
)

// limitBody caps r.Body at n bytes. Reads past the cap fail with
// *http.MaxBytesError, which the decode helpers turn into 413.
func limitBody(n int64, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next(w, r)
	}
}

// isTooLarge reports whether err came from a body over its limit.
func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// writeTooLarge answers 413.
func writeTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
}

// decodeJSON decodes the request body into v. On failure it writes 413 for
// an oversized body or 400 otherwise, and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid json")
		return false
	}
	return true
}
