package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/aieera/sedoc/pkg/middleware"
)

// documentBodyPrefix is the only route family that carries document bytes
// (scan-capture bundles); everything else is small JSON.
const documentBodyPrefix = "/api/v1/connectors/capture/"

// withBodyLimits applies the DEC-19 body caps: 64 MiB on the capture routes,
// 1 MiB on every other route. A declared Content-Length over the cap is
// refused with 413 before the handler runs; a chunked body is cut off by
// http.MaxBytesReader.
func withBodyLimits(h http.Handler) http.Handler {
	jsonLimited := middleware.MaxBody(middleware.MaxJSONBody)(h)
	docLimited := middleware.MaxBody(middleware.MaxDocumentBody)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, documentBodyPrefix) {
			docLimited.ServeHTTP(w, r)
			return
		}
		jsonLimited.ServeHTTP(w, r)
	})
}

// newHTTPServer sets the DEC-19 server timeouts. Handlers that legitimately
// write for longer (the SSE tail, Drive import, capture) extend their own
// write deadline with http.ResponseController — a per-route exemption, not
// a server-wide one.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
