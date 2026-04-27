package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	vdmsmw "github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/auth/internal/scim"
)

// Router wires all auth HTTP routes. Public routes (register, login, MFA
// verify, MFA recovery, SAML metadata/login/acs) are unauthenticated;
// everything else requires a valid session via AuthMiddleware.
//
// SCIMWiring bundles the optional SCIM surface. Pass nil to omit.
type SCIMWiring struct {
	Handler  *scim.Handler
	Resolver *scim.TenantResolver
}

// Router wires all auth HTTP routes. Each optional subsystem may be nil.
func (h *Handler) Router(saml *SAMLHandler, oidc *OIDCHandler, sc *SCIMWiring, groups *GroupsHandler, ssoAdmin *SSOAdminHandler, tenantAdmin *TenantAdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)

	r.Route("/api/v1/auth", func(r chi.Router) {
		// ---- Public -------------------------------------------------------
		r.With(vdmsmw.NewIPRateLimiter(5, 5, time.Minute)).Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.Post("/mfa/verify", h.MFAVerify)
		r.Post("/mfa/recovery", h.MFARecovery)
		// Wave 15.3: one-time-token-authenticated, so public.
		r.With(vdmsmw.NewIPRateLimiter(5, 5, time.Minute)).Post("/change-password", h.ChangePassword)
		// GAP-3: Forgot password — unauthenticated, rate-limited.
		// Always returns 202 with a generic message; the service
		// handles its own oracle defense (unknown slug/email/SSO
		// user are all silent no-ops, only the success path emits
		// dms.notify.password_reset.v1).
		r.With(vdmsmw.NewIPRateLimiter(5, 5, time.Minute)).Post("/forgot-password", h.ForgotPassword)

		// ---- SAML 2.0 SSO (public; tenant identified by path slug) -------
		if saml != nil {
			r.Route("/saml/{tenant_slug}", func(r chi.Router) {
				r.Get("/metadata", saml.Metadata)
				// Rate-limit SSO initiation by IP to slow AuthnRequest flooding.
				r.With(vdmsmw.NewIPRateLimiter(30, 10, time.Minute)).Get("/login", saml.Login)
				r.With(vdmsmw.NewIPRateLimiter(30, 10, time.Minute)).Post("/acs", saml.ACS)
			})
		}

		// ---- OIDC SSO (public; tenant identified by path slug) -----------
		if oidc != nil {
			r.Route("/oidc/{tenant_slug}", func(r chi.Router) {
				r.With(vdmsmw.NewIPRateLimiter(30, 10, time.Minute)).Get("/login", oidc.Login)
				r.With(vdmsmw.NewIPRateLimiter(30, 10, time.Minute)).Get("/callback", oidc.Callback)
			})
		}

		// ---- Authenticated (session OR API key) --------------------------
		// CSRF middleware runs after auth: it exempts GET/HEAD/OPTIONS
		// and Bearer-authed API-key callers; session-cookie callers
		// must present a matching X-CSRF-Token header on mutations.
		r.Group(func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())

			r.Get("/me", h.Me)
			r.Post("/logout", h.Logout)

			r.Route("/sessions", func(r chi.Router) {
				r.Get("/", h.ListSessions)
				r.Post("/revoke-all", h.RevokeAllSessions)
				// Blueprint §8.1 spec also accepts DELETE on the
				// collection as "revoke all but current". Same
				// semantics as POST /revoke-all; alias kept so clients
				// using either idiom work.
				r.Delete("/", h.RevokeAllSessions)
				r.Delete("/{session_id}", h.RevokeSession)
			})

			r.Route("/mfa", func(r chi.Router) {
				r.Post("/setup", h.MFASetup)
				r.Post("/confirm", h.MFAConfirm)
				r.Post("/disable", h.MFADisable)
			})

			r.Route("/api-keys", func(r chi.Router) {
				// Admin-only for issuing keys.
				r.With(h.RequireRole("admin", "owner")).Post("/", h.IssueAPIKey)
				r.Get("/", h.ListAPIKeys)
				r.Delete("/{key_id}", h.RevokeAPIKey)
			})
		})
	})

	// ---- Admin password policy (Wave 15.3) -------------------------------
	r.Route("/api/v1/admin/password-policy", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("owner"))
		r.Post("/sweep-expired", h.SweepExpiredPasswordsAdmin)
	})

	// ---- Tenant session policy (Blueprint §8.1) ---------------------------
	// GET is authenticated-only so admins can render a read-only view;
	// PUT is owner-gated (tenant-wide security policy change).
	r.Route("/api/v1/tenant/session-policy", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Get("/", h.sessionPolicy.GetSessionPolicy)
		r.With(h.RequireRole("owner")).Put("/", h.sessionPolicy.PutSessionPolicy)
	})

	// Plan lookup for the upload-tier tooltip + billing surfaces.
	r.Route("/api/v1/tenant/plan", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Get("/", h.GetTenantPlan)
	})

	// ---- Tenant residency policy (Blueprint §9.1) -------------------------
	// GET is authenticated-only — region pickers across the app need to
	// know the allowlist + default to render correctly. PUT is
	// owner-gated; allowing admins to flip the policy lets a single
	// account-takeover quietly migrate every new doc to a different
	// jurisdiction.
	r.Route("/api/v1/tenant/residency-policy", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Get("/", h.GetTenantResidencyPolicy)
		r.With(h.RequireRole("owner")).Put("/", h.PutTenantResidencyPolicy)
	})

	// Per-workspace region overrides — read-only listing. Admin UI
	// surfaces these as a table; editing is done from each workspace's
	// settings page (document service owns those routes).
	r.Route("/api/v1/tenant/workspace-regions", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Get("/", h.ListWorkspaceRegions)
	})

	// ---- Admin user management ------------------------------------------------
	// Frontend calls /api/v1/admin/users/* — gated by the user's role.
	r.Route("/api/v1/admin/users", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("admin", "owner"))
		r.Get("/", h.ListUsersAdmin)
		r.Post("/invite", h.InviteUserAdmin)
		r.Post("/{id}/suspend", h.SuspendUserAdmin)
		r.Post("/{id}/reset-mfa", h.ResetUserMFAAdmin)
		r.Post("/{id}/force-password-reset", h.ForcePasswordResetAdmin)
	})

	// ---- Admin groups (Wave 10) ------------------------------------------
	if groups != nil {
		r.Route("/api/v1/admin/groups", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("admin", "owner"))
			groups.Mount(r)
		})
	}

	// ---- Admin SSO configs (Wave 10) -------------------------------------
	if ssoAdmin != nil {
		r.Route("/api/v1/admin/sso-configs", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("admin", "owner"))
			ssoAdmin.Mount(r)
		})
	}

	// ---- Tenant control-plane (Wave 12.7) --------------------------------
	// Owner-only: region-pin + CMK scheduled-deletion.
	if tenantAdmin != nil {
		r.Route("/api/v1/admin/tenants", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("owner"))
			tenantAdmin.Mount(r)
		})
	}

	// ---- SCIM 2.0 /scim/v2/{tenant_slug}/* ---------------------------------
	// Per-tenant bearer token auth; the resolver looks up the tenant by slug
	// and validates the token hash stored in sso_configs.config.
	if sc != nil && sc.Handler != nil && sc.Resolver != nil {
		r.Route("/scim/v2/{tenant_slug}", func(r chi.Router) {
			r.Use(scimAuthMiddleware(sc.Resolver))
			sc.Handler.Mount(r)
		})
	}

	return r
}

// scimAuthMiddleware extracts the path's tenant_slug and delegates to the
// resolver. Factored out so the route body reads cleanly.
func scimAuthMiddleware(res *scim.TenantResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := chi.URLParam(r, "tenant_slug")
			res.Authenticate(slug)(next).ServeHTTP(w, r)
		})
	}
}
