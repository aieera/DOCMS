package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/license"
)

func licOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
}

// TestLicenseWriteGate: grace/expired locks writes (423), reads pass; active +
// unlicensed-dev pass everything.
func TestLicenseWriteGate(t *testing.T) {
	orig := licenseStatusFn
	t.Cleanup(func() { licenseStatusFn = orig })

	cases := []struct {
		status     license.Status
		method     string
		wantStatus int
	}{
		{license.StatusActive, http.MethodPost, 200},
		{license.StatusUnlicensedDev, http.MethodPost, 200},
		{license.StatusGrace, http.MethodGet, 200},  // reads pass in grace
		{license.StatusGrace, http.MethodPost, 423}, // writes locked
		{license.StatusGrace, http.MethodDelete, 423},
		{license.StatusExpired, http.MethodPut, 423},
		{license.StatusExpired, http.MethodGet, 200},
	}
	for _, tc := range cases {
		licenseStatusFn = func() license.Status { return tc.status }
		rec := httptest.NewRecorder()
		LicenseWriteGate(licOK()).ServeHTTP(rec, httptest.NewRequest(tc.method, "/x", nil))
		if rec.Code != tc.wantStatus {
			t.Errorf("status=%s method=%s: want %d, got %d", tc.status, tc.method, tc.wantStatus, rec.Code)
		}
	}
}

// TestRequireLicenseFeature: 402 when the flag is off, pass when on, pass in
// unlicensed-dev (nil claims).
func TestRequireLicenseFeature(t *testing.T) {
	orig := licenseClaimsFn
	t.Cleanup(func() { licenseClaimsFn = orig })

	future := time.Now().Add(24 * time.Hour)

	// Feature on.
	licenseClaimsFn = func() *license.Claims {
		return &license.Claims{ExpiresAt: future, FeatureFlags: license.FeatureFlags{Esign: true}}
	}
	rec := httptest.NewRecorder()
	RequireLicenseFeature("esign")(licOK()).ServeHTTP(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != 200 {
		t.Fatalf("feature on: want 200, got %d", rec.Code)
	}

	// Feature off.
	licenseClaimsFn = func() *license.Claims {
		return &license.Claims{ExpiresAt: future, FeatureFlags: license.FeatureFlags{Esign: false}}
	}
	rec = httptest.NewRecorder()
	RequireLicenseFeature("esign")(licOK()).ServeHTTP(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("feature off: want 402, got %d", rec.Code)
	}

	// Unlicensed-dev (nil claims) → HasFeature true → pass.
	licenseClaimsFn = func() *license.Claims { return nil }
	rec = httptest.NewRecorder()
	RequireLicenseFeature("mcp")(licOK()).ServeHTTP(rec, httptest.NewRequest("POST", "/x", nil))
	if rec.Code != 200 {
		t.Fatalf("unlicensed-dev: want 200, got %d", rec.Code)
	}
}
