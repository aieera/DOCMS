package activities

import (
	"errors"
	"net/http"
	"os"

	"github.com/aieera/sedoc/pkg/middleware"
)

// errInternalKeyMissing is returned instead of sending a service-to-service
// call that cannot authenticate: the receiver would answer an opaque 401
// (or, before DEC-3, act with no identity).
var errInternalKeyMissing = errors.New("SEDOC_INTERNAL_API_KEY is not configured: workflow cannot authenticate service-to-service calls")

// authorizeServiceCall stamps the credentials every workflow → sibling
// service HTTP call needs (DEC-3, C-6, M-13):
//
//   - X-Gateway-Signature: the receivers sit behind RequireGatewaySignature
//     (the gateway is bypassed on the internal network, so the signature
//     must be presented by the caller).
//   - X-Internal-Service-Key: proof the caller is a trusted backend. Under
//     DEC-3 the identity headers below are honoured ONLY together with it
//     and only when no session resolved (a session always wins); without it
//     the receiver sees an anonymous request.
//   - X-Auth-Tenant-ID / X-User-ID / X-User-Role: the identity the activity
//     acts for. userID is optional. role is omitted when empty, but a
//     SessionOrAPIKey receiver REQUIRES a known X-User-Role with the internal
//     key (missing/unknown → 401), so calls to such routes must pass one —
//     the acting user's least-privileged role. Receivers behind
//     IdentityHeadersHTTP + requireInternalKey (e.g. /internal/v1/*) do not.
func (a *Activities) authorizeServiceCall(req *http.Request, tenantID, userID, role string) error {
	if a.InternalKey == "" {
		return errInternalKeyMissing
	}
	req.Header.Set(middleware.GatewaySignatureHeader, os.Getenv(middleware.GatewaySignatureEnv))
	req.Header.Set(middleware.InternalServiceKeyHeader, a.InternalKey)
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	if role != "" {
		req.Header.Set("X-User-Role", role)
	}
	return nil
}
