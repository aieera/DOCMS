package middleware

import (
	"net/http"
	"time"
)

// §12.1 / J1 — public-API versioning + deprecation headers.
//
// APIVersion sets a stable semver on every response in the
// /api/v1/* surface. Clients pin on major and surface mismatches.
//
// Deprecate is a route-level helper for endpoints that will be
// removed. RFC 9745 headers are emitted so standard-compliant
// clients can parse + alert.

// CurrentAPIVersion is the SemVer the whole /api/v1 surface
// advertises. Bump MINOR on additive changes, MAJOR only with a
// parallel /api/v2 deployment (docs/api/SEMVER_POLICY.md).
const CurrentAPIVersion = "1.0.0"

// APIVersion returns middleware that stamps every response with
// the `API-Version` header. Wrap the root handler once; every route
// inherits it.
func APIVersion(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", CurrentAPIVersion)
		next.ServeHTTP(w, r)
	})
}

// Deprecate writes the three RFC 9745 headers on every response
// from a wrapped handler. Call from a single route's register
// path — NOT globally — because the Deprecation date MUST be
// route-specific.
//
//	mux.Handle("GET /api/v1/legacy/thing",
//	    middleware.Deprecate(
//	        time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
//	        time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC),
//	        "https://docs.vaultdms.example/migrate/legacy-thing",
//	    )(legacyThingHandler),
//	)
func Deprecate(deprecationDate, sunsetDate time.Time, migrationLink string) func(http.Handler) http.Handler {
	deprecationHeader := deprecationDate.UTC().Format(http.TimeFormat)
	sunsetHeader := sunsetDate.UTC().Format(http.TimeFormat)
	linkHeader := `<` + migrationLink + `>; rel="deprecation"`
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Deprecation", deprecationHeader)
			w.Header().Set("Sunset", sunsetHeader)
			if migrationLink != "" {
				w.Header().Add("Link", linkHeader)
			}
			// Past the sunset date the endpoint MUST return 410 Gone —
			// this middleware does it automatically so handlers don't
			// have to remember the date.
			if !time.Now().Before(sunsetDate) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusGone)
				_, _ = w.Write([]byte(
					`{"error":"sunset","message":"this endpoint is no longer available; see the Link header for the replacement"}`,
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
