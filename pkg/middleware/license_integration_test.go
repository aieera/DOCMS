package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aieera/sedoc/pkg/license"
)

// TestLicenseRealPipeline exercises the REAL license pipeline end-to-end:
// license.Init() parses + RS256-verifies SEDOC_LICENSE_JWT against the bundled
// public key, and the wired middleware reacts to the actual derived status +
// feature flags (no injected accessors). Skipped when no JWT is set so CI stays
// green; run locally with a minted license, e.g.:
//
//	SEDOC_LICENSE_JWT=$(cat grace.jwt)   go test -run RealPipeline ./pkg/middleware/ -v
//	SEDOC_LICENSE_JWT=$(cat noipaas.jwt) go test -run RealPipeline ./pkg/middleware/ -v
//
// Self-adapting: it asserts the middleware matches whatever license loaded.
func TestLicenseRealPipeline(t *testing.T) {
	if os.Getenv("SEDOC_LICENSE_JWT") == "" {
		t.Skip("no SEDOC_LICENSE_JWT; skipping real-pipeline enforcement check")
	}
	if err := license.Init(); err != nil {
		t.Fatalf("license.Init: %v", err)
	}
	status := license.CurrentStatus()
	t.Logf("loaded license: status=%s claims=%+v", status, license.Current())

	// Write-gate: 423 once in grace/expired, else pass.
	rec := httptest.NewRecorder()
	LicenseWriteGate(licOK()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	wantWrite := http.StatusOK
	if status == license.StatusGrace || status == license.StatusExpired {
		wantWrite = http.StatusLocked
	}
	if rec.Code != wantWrite {
		t.Errorf("write-gate POST: status=%s want %d got %d", status, wantWrite, rec.Code)
	}
	// Reads always pass the write-gate.
	rec = httptest.NewRecorder()
	LicenseWriteGate(licOK()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("write-gate GET: want 200 got %d", rec.Code)
	}

	// Feature gates: 402 when the flag is off, pass when on — matching the
	// loaded license's actual flags.
	for _, f := range []string{"esign", "ipaas", "mcp", "intel_llm"} {
		rec := httptest.NewRecorder()
		RequireLicenseFeature(f)(licOK()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		want := http.StatusOK
		if !license.Current().HasFeature(f) {
			want = http.StatusPaymentRequired
		}
		if rec.Code != want {
			t.Errorf("feature %q (licensed=%v): want %d got %d", f, license.Current().HasFeature(f), want, rec.Code)
		}
	}
}
