package handler

import (
	"net/http"

	"github.com/aieera/sedoc/pkg/middleware"
)

// MaxJSONBody is the DEC-19 request-body cap for JSON routes (1 MiB),
// aliased from pkg/middleware so the document service and the shared
// middleware can never disagree on the figure.
const MaxJSONBody = middleware.MaxJSONBody

// limitBody caps the request body at n bytes via middleware.MaxBody
// (DEC-19): a declared Content-Length above n is 413 before the handler
// runs, and the body is wrapped in http.MaxBytesReader so an undeclared
// (chunked) oversize body fails the handler's read.
func limitBody(n int64, next http.Handler) http.Handler {
	return middleware.MaxBody(n)(next)
}

// limitJSON is limitBody(MaxJSONBody) for a HandlerFunc.
func limitJSON(fn http.HandlerFunc) http.Handler { return limitBody(MaxJSONBody, fn) }

// writeBodyReadError maps a body read/decode failure: an exceeded
// MaxBytesReader limit is 413, anything else 400.
func writeBodyReadError(w http.ResponseWriter, err error) {
	if middleware.BodyTooLarge(err) {
		writeJSONStatus(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		return
	}
	writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
}
