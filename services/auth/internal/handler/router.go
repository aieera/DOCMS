package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	vdmsmw "github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/auth/internal/scim"
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
func (h *Handler) Router(saml *SAMLHandler, oidc *OIDCHandler, sc *SCIMWiring, groups *GroupsHandler, ssoAdmin *SSOAdminHandler, scimAdmin *SCIMAdminHandler, encAdmin *EncryptionAdminHandler, tenantAdmin *TenantAdminHandler, ldapAdmin *LDAPAdminHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)

	r.Route("/api/v1/auth", func(r chi.Router) {
		// ---- Public -------------------------------------------------------
		r.With(vdmsmw.NewIPRateLimiter(5, 5, time.Minute)).Post("/register", h.Register)
		r.With(vdmsmw.NewIPRateLimiter(10, 5, time.Minute)).Post("/accept-invite", h.AcceptInvite)
		r.Post("/login", h.Login)
		// Track 2 — password reset. Public + per-IP rate-limited (the service
		// adds a per-email throttle + a constant-time floor for enumeration).
		r.With(vdmsmw.NewIPRateLimiter(5, 3, time.Minute)).Post("/forgot-password", h.ForgotPassword)
		r.With(vdmsmw.NewIPRateLimiter(10, 5, time.Minute)).Post("/reset-password", h.ResetPassword)
		r.Post("/mfa/verify", h.MFAVerify)
		r.Post("/mfa/recovery", h.MFARecovery)
		// ADR 0112 — Outlook add-in SSO exchange. Public on purpose:
		// this IS the entry point that establishes auth, and the body
		// carries an Entra ID token we validate via Graph.
		r.With(vdmsmw.NewIPRateLimiter(10, 5, time.Minute)).Post("/m365/exchange", h.ExchangeM365)
		// ADR 0116 — Google Workspace add-on SSO exchange. Same
		// public-entry-point rationale: the body carries a Google
		// OIDC ID token we verify against Google’s JWKS.
		r.With(vdmsmw.NewIPRateLimiter(10, 5, time.Minute)).Post("/google/exchange", h.ExchangeGoogle)

		// ADR 0063 — multi-method MFA, public (post-password,
		// gated by mfa_session_token).
		r.Post("/mfa/methods", h.MFAListMethods)
		r.With(vdmsmw.NewIPRateLimiter(10, 5, time.Minute)).Post("/mfa/email/start", h.MFAEmailStart)
		r.Post("/mfa/email/verify", h.MFAEmailVerify)
		r.With(vdmsmw.NewIPRateLimiter(5, 5, time.Minute)).Post("/mfa/sms/start", h.MFASMSStart)
		r.Post("/mfa/sms/verify", h.MFASMSVerify)
		r.Post("/mfa/push/start", h.MFAPushStart)
		r.Post("/mfa/push/verify", h.MFAPushVerify)

		// ADR 0061 — passkey login (public; no prior session needed).
		// Same rate-limit class as password login since they serve the
		// same auth-attempt role.
		r.With(vdmsmw.NewIPRateLimiter(20, 10, time.Minute)).Post("/webauthn/login/begin", h.WebAuthnLoginStart)
		r.With(vdmsmw.NewIPRateLimiter(20, 10, time.Minute)).Post("/webauthn/login/finish", h.WebAuthnLoginFinish)

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
			// ADR 0106 — self-service locale picker (LanguageSelector).
			r.Patch("/me/locale", h.UpdateMyLocale)
			r.Post("/logout", h.Logout)

			r.Route("/sessions", func(r chi.Router) {
				r.Get("/", h.ListSessions)
				r.Post("/revoke-all", h.RevokeAllSessions)
				r.Delete("/{session_id}", h.RevokeSession)
			})

			r.Route("/mfa", func(r chi.Router) {
				r.Post("/setup", h.MFASetup)
				r.Post("/confirm", h.MFAConfirm)
				r.Post("/disable", h.MFADisable)

				// ADR 0063 — authenticated enrollment + management.
				r.Get("/methods", h.MFAListMine)
				r.Post("/email/enroll", h.MFAEnrollEmail)
				r.Post("/sms/enroll", h.MFAEnrollSMS)
				r.Post("/push/devices", h.MFARegisterPushDevice)
				r.Delete("/methods/{method}", h.MFADisableMethod)
			})

			r.Route("/api-keys", func(r chi.Router) {
				// Admin-only for issuing keys.
				r.With(h.RequireRole("admin", "owner")).Post("/", h.IssueAPIKey)
				r.Get("/", h.ListAPIKeys)
				r.Delete("/{key_id}", h.RevokeAPIKey)
			})

			// ADR 0061 — passkey registration + management (authed).
			r.Route("/webauthn", func(r chi.Router) {
				r.Post("/registration/begin", h.WebAuthnRegistrationStart)
				r.Post("/registration/finish", h.WebAuthnRegistrationFinish)
				r.Get("/credentials", h.WebAuthnList)
				r.Delete("/credentials/{id}", h.WebAuthnDelete)
			})
		})
	})

	// ---- Admin user management ------------------------------------------------
	// Frontend calls /api/v1/admin/users/* — gated by the user's role.
	r.Route("/api/v1/admin/users", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("admin", "owner"))
		r.Get("/", h.ListUsersAdmin)
		r.Get("/seat-usage", h.SeatUsageAdmin)
		r.Post("/", h.CreateUserAdmin)
		r.Post("/invite", h.InviteUserAdmin)
		r.Post("/{id}/suspend", h.SuspendUserAdmin)
		r.Post("/{id}/reactivate", h.ReactivateUserAdmin)
		r.Post("/{id}/reset-mfa", h.ResetUserMFAAdmin)
		r.Patch("/{id}/role", h.ChangeUserRoleAdmin)
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

	// ---- Admin SCIM (base URL, token rotate, provisioning log) -----------
	if scimAdmin != nil {
		r.Route("/api/v1/admin/scim", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("admin", "owner"))
			scimAdmin.Mount(r)
		})
	}

	// ---- Admin encryption / external KMS --------------------------------
	if encAdmin != nil {
		r.Route("/api/v1/admin/encryption", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("admin", "owner"))
			encAdmin.Mount(r)
		})
	}

	// ---- Admin MFA policy (ADR 0063) ------------------------------------
	r.Route("/api/v1/admin/mfa", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("admin", "owner"))
		r.Get("/policy", h.MFAGetPolicy)
		r.Put("/policy", h.MFAPutPolicy)
	})

	// Per-tenant notification provider credentials (Twilio, SMTP).
	// Same shape as the eSign per-tenant configs; saved via the admin
	// UI Notifications tab. SMTP write path lives in the notification
	// service; the GET here is auth's read-only view of the same row
	// so the modal can pre-fill.
	r.Route("/api/v1/admin/notifications", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("admin", "owner"))
		r.Get("/twilio", h.GetTwilioConfig)
		r.Put("/twilio", h.PutTwilioConfig)
		r.Delete("/twilio", h.DeleteTwilioConfig)
		r.Post("/twilio/test", h.TestTwilioConfig)
		r.Get("/smtp", h.GetSMTPConfig)
		r.Put("/smtp", h.PutSMTPConfig)
		r.Delete("/smtp", h.DeleteSMTPConfig)
		r.Post("/smtp/test", h.TestSMTPConfig)
	})

	// ---- Admin LDAP / AD direct bind (ADR 0062) -------------------------
	if ldapAdmin != nil {
		r.Route("/api/v1/admin/ldap", func(r chi.Router) {
			r.Use(h.AuthMiddleware)
			r.Use(vdmsmw.CSRFDoubleSubmit())
			r.Use(h.RequireRole("admin", "owner"))
			ldapAdmin.Mount(r)
		})
	}

	// ---- ERP integration files proxy ------------------------------------
	// Same-origin bridge so the native "Integrations → ERP" explorer (which
	// runs on the SeDoc session) can reach the ERP integration "Files BFF".
	// That BFF speaks a different (ERP-user) auth model and holds the SeDoc
	// service API key, so the browser can't call it directly. Admin/owner only;
	// IntegrationERPProxy stamps the ERP identity server-side and rewrites
	// /api/v1/integrations/erp/<rest> → <bff>/files/<rest>. CSRF middleware
	// exempts the GET reads this explorer uses.
	r.Route("/api/v1/integrations/erp", func(r chi.Router) {
		r.Use(h.AuthMiddleware)
		r.Use(vdmsmw.CSRFDoubleSubmit())
		r.Use(h.RequireRole("admin", "owner"))
		r.Handle("/*", http.HandlerFunc(h.IntegrationERPProxy))
	})

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
