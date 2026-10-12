package main

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/workflow/internal/handler"
)

// userAPIScope is the scope an API key must hold to reach the user-facing
// workflow API. No issued key carries it today (it is not in the auth
// service's ValidScopes), so in practice the API is session-only for
// external callers; internal services use X-Internal-Service-Key. It exists
// so a future integration scope is an explicit decision.
const userAPIScope = "workflows:write"

// newRouter assembles the workflow service's HTTP surface. triggers is the
// fully-wrapped iPaaS trigger handler (APIKeyAuth + rate limit + license).
//
// Authentication (review finding C-6): every /api/v1/workflows/ route sits
// behind SessionOrAPIKey, which takes identity from the session
// cookie/bearer, an API key, or the internal-service key — never from the
// gateway identity headers alone. IdentityHeadersHTTP runs only INSIDE that
// envelope, where it can gap-fill but never replace an authenticated
// identity. Before this fix the service mounted only RequireGatewaySignature
// → IdentityHeadersHTTP, so a request through the host-mode Vite proxy with
// forged X-Tenant-ID / X-User-ID / X-User-Role headers and no cookie could
// approve steps, cancel instances and rewrite templates as anyone.
//
// The only route outside the envelope is the iPaaS trigger, behind its own
// APIKeyAuth. Anything else that is not a workflow route is answered 401 by
// the same envelope (no 404-vs-401 existence oracle).
func newRouter(pool *pgxpool.Pool, h *handler.Handler, triggers http.Handler) http.Handler {
	api := http.NewServeMux()
	h.Register(api)
	// ADR 0064 — tenant-wide delegations + recall.
	h.RegisterRoutingPatterns(api)

	userAuth := middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, userAPIScope)

	root := http.NewServeMux()
	// DEC-19: the workflow API is JSON-only; cap bodies at 1 MiB before the
	// session lookup or any handler reads them.
	root.Handle("/", middleware.MaxBody(middleware.MaxJSONBody)(userAuth(middleware.IdentityHeadersHTTP()(api))))

	// ADR 0090 — iPaaS trigger endpoint (Zapier / Make / n8n). API-key
	// authenticated; the /api/v1/integrations/triggers/ prefix is
	// whitelisted by RequireGatewaySignature.
	root.Handle("/api/v1/integrations/triggers/workflows/completed", triggers)

	// License grace write-gate (ADR 0095): mutating requests → 423 in grace/
	// expired; reads pass. No-op when active/unlicensed-dev.
	return middleware.RequireGatewaySignature()(middleware.LicenseWriteGate(root))
}
