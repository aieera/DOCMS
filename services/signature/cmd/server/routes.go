package main

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/signature/internal/handler"
)

// userAPIScope is the scope an API key must hold to reach the user-facing
// signature API. No issued key carries it today, so in practice the user API
// is session-only; it exists so a future integration scope is explicit.
const userAPIScope = "signatures:write"

// newRouter assembles the signature service's HTTP surface. triggers is the
// fully-wrapped iPaaS trigger handler (APIKeyAuth + rate limit + license).
//
// Authentication (review finding C-6): every user-facing route sits behind
// SessionOrAPIKey, which takes identity from the session cookie/bearer, an
// API key, or the internal-service key — never from the gateway identity
// headers alone. IdentityHeadersHTTP runs only INSIDE that envelope, where it
// can gap-fill but never replace an authenticated identity; before this fix
// it ran outermost, so a request through the host-mode Vite proxy with forged
// X-Tenant-ID / X-User-ID / X-User-Role headers and no cookie could create,
// sign and cancel requests as anyone. The only routes outside the envelope:
//   - the vendor webhook, authenticated by the provider's body HMAC;
//   - the internal seal endpoints, behind their own SessionOrAPIKey;
//   - the iPaaS trigger, behind APIKeyAuth.
func newRouter(pool *pgxpool.Pool, h *handler.Handler, triggers http.Handler) http.Handler {
	api := http.NewServeMux()
	h.Register(api)
	// Frontend lands on /sign/done after the QTSP redirect; the page
	// polls /qes/session/:id for the final status.
	h.RegisterQES(api, "/sign/done")
	// esign (external DocuSign/AdobeSign integration) is the `esign` licensed
	// feature. All its routes share the /api/v1/signatures/esign/ prefix, so
	// register them on a sub-mux and gate the whole subtree — 402 when esign
	// isn't licensed; no-op in unlicensed-dev.
	esignMux := http.NewServeMux()
	h.RegisterESign(esignMux)
	api.Handle("/api/v1/signatures/esign/", middleware.RequireLicenseFeature("esign")(esignMux))
	// ADR 0073 — in-person tablet ceremony (single device, sequential
	// signer + witness on the same session).
	h.RegisterInPerson(api)

	userAuth := middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, userAPIScope)

	root := http.NewServeMux()
	root.Handle("/", userAuth(middleware.IdentityHeadersHTTP()(api)))

	// Vendor webhook: no session (the vendor calls us), authenticated by the
	// provider's body HMAC inside the handler.
	webhookMux := http.NewServeMux()
	h.RegisterESignWebhook(webhookMux)
	root.Handle("/api/v1/signatures/esign/webhook/", middleware.RequireLicenseFeature("esign")(webhookMux))

	// ADR 0025 — internal server-seal endpoint. Sub-mux gated by the
	// internal-service key (SessionOrAPIKey); the path is whitelisted from the
	// gateway-sig check so trusted services can call it directly, and the
	// gateway strips client-supplied internal keys so external callers can't.
	sealMux := http.NewServeMux()
	h.RegisterSeal(sealMux)
	sealAuth := middleware.SessionOrAPIKey(middleware.SessionAuthConfig{Pool: pool}, "signatures:write")(sealMux)
	// Exact-path mounts (Go ServeMux without a trailing slash matches exactly):
	// the org seal and the workflow-owned ceremony seal (ADR 0025 Wave 9).
	root.Handle("/api/v1/signatures/internal/seal", sealAuth)
	root.Handle("/api/v1/signatures/internal/seal-ceremony", sealAuth)

	// ADR 0090 — iPaaS trigger endpoint (Zapier / Make / n8n).
	root.Handle("/api/v1/integrations/triggers/signatures/completed", triggers)

	// License grace write-gate (ADR 0095): mutating requests → 423 in grace/
	// expired; reads pass. No-op when active/unlicensed-dev.
	return middleware.RequireGatewaySignature()(middleware.LicenseWriteGate(root))
}
