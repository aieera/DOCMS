package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/license"
)

// License enforcement middleware (ADR 0095 — Phase 3).
//
// pkg/license loads + validates the JWT and exposes the derived Status +
// feature flags; this is the HTTP layer that ACTS on it. Two gates:
//
//   - LicenseWriteGate: once the license is past expiry (grace or expired),
//     mutating requests are locked (423) while reads stay open, so a tenant
//     can still export + wind down during the grace window.
//   - RequireLicenseFeature: gates a route subtree on a feature flag, 402 when
//     the feature isn't licensed.
//
// Both no-op in unlicensed-dev mode (no JWT) so local/CI workflows don't
// regress — license.Current() is nil there and HasFeature returns true.
//
// The accessors are package vars so tests can drive status/claims without the
// process-wide license state.
var (
	licenseStatusFn = license.CurrentStatus
	licenseClaimsFn = license.Current
)

type licenseErrBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeLicenseError(w http.ResponseWriter, status int, code, msg string) {
	var b licenseErrBody
	b.Error.Code = code
	b.Error.Message = msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(b)
}

func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// LicenseWriteGate blocks mutating requests (POST/PUT/PATCH/DELETE) with
// 423 Locked when the license is in grace or expired; reads pass. Active +
// unlicensed-dev pass through. Fully-expired startup refusal is a separate
// boot concern (SEDOC_REQUIRE_LICENSE).
func LicenseWriteGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutatingMethod(r.Method) {
			switch licenseStatusFn() {
			case license.StatusGrace, license.StatusExpired:
				writeLicenseError(w, http.StatusLocked, "license_locked",
					"License expired — writes are locked. Renew to restore write access.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireLicenseFeature gates a route subtree on a license feature flag.
// Returns 402 Payment Required when the feature isn't licensed. Unlicensed-dev
// passes (HasFeature is true on nil claims).
func RequireLicenseFeature(feature string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !licenseClaimsFn().HasFeature(feature) {
				writeLicenseError(w, http.StatusPaymentRequired, "feature_not_licensed",
					"The \""+feature+"\" feature is not included in your license.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
