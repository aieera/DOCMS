package main

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/middleware"
)

// newHTTPHandler wraps the policy REST mux with the gateway-signature,
// body-size (DEC-19: 1 MiB JSON) and session middleware. Split out of main
// so the wiring is testable.
func newHTTPHandler(pool *pgxpool.Pool, mux http.Handler) http.Handler {
	return middleware.RequireGatewaySignature()(
		middleware.MaxBody(middleware.MaxJSONBody)(
			middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(mux)))
}
