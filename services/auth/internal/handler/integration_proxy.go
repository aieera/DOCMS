package handler

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/aieera/sedoc/pkg/auth"
)

// IntegrationERPProxy is the same-origin bridge between the SeDoc web app and
// the ERP integration "Files BFF" (the service on :8091 that the standalone
// explorer used to talk to behind an iframe).
//
// The two have incompatible auth models: the SeDoc app authenticates with a
// session cookie, while the Files BFF trusts an X-ERP-User header (stamped by
// the ERP's proxy in production) and holds the SeDoc service API key
// server-side. The browser therefore cannot call the BFF directly. This proxy
// closes the gap: the SeDoc session is the authoritative gate (AuthMiddleware +
// RequireRole("admin","owner") run before this handler), and we stamp the ERP
// identity headers here, server-side, so the secret-bearing BFF stays out of
// the browser's reach.
//
// Path rewrite:  /api/v1/integrations/erp/<rest>  →  <bff>/files/<rest>
func (h *Handler) IntegrationERPProxy(w http.ResponseWriter, r *http.Request) {
	target, err := url.Parse(h.integrationBFFURL)
	if err != nil || target.Host == "" {
		h.writeJSON(w, http.StatusInternalServerError,
			map[string]string{"error": "integration BFF URL misconfigured"})
		return
	}

	// X-ERP-User must be non-empty or the BFF 401s. The role is already gated
	// to admin/owner by the route middleware, so we stamp X-ERP-Admin:true —
	// the BFF treats that as an authz override (a SeDoc operator browsing the
	// integration natively sees every provisioned customer).
	user, _ := auth.User(r.Context())
	erpUser := user.Email
	if erpUser == "" {
		erpUser = user.ID.String()
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			rest := strings.TrimPrefix(req.URL.Path, "/api/v1/integrations/erp")
			if !strings.HasPrefix(rest, "/") {
				rest = "/" + rest
			}
			req.URL.Path = "/files" + rest
			// Stamp the ERP identity server-side; the browser never sets these.
			req.Header.Set("X-ERP-User", erpUser)
			req.Header.Set("X-ERP-Admin", "true")
			// Strip SeDoc-only credentials — they're meaningless to the BFF and
			// must not leak past this hop.
			req.Header.Del("Cookie")
			req.Header.Del("X-Gateway-Signature")
			req.Header.Del("X-Csrf-Token")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.log.Error().Err(err).Msg("integration files bff proxy")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"integration service unreachable"}`))
		},
	}
	proxy.ServeHTTP(w, r)
}
