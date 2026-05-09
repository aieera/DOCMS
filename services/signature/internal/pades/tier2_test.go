//go:build tier2

// Tier-2 conformance check: feed a known-good signed PDF to the
// EU Commission DSS demo validator and assert TOTAL_PASSED.
//
// Build-tag-gated so it doesn't run in default `make test` —
// the validator is a remote service with rate limits + privacy
// implications (don't feed customer documents through it).
//
// Triggered nightly by the same workflow that runs the sandbox
// tests; see docs/runbooks/pades-ltv-tier2.md.
package pades_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const euDSSValidator = "https://ec.europa.eu/digital-building-blocks/DSS/webapp-demo/services/rest/validation/validateSignature"

// TestTier2_EUValidator submits a B-LT-grade fixture and asserts
// the EU DSS validator returns TOTAL_PASSED. The fixture path is
// configurable via PADES_TIER2_FIXTURE so each release can point
// at a freshly-stamped PDF without code change.
func TestTier2_EUValidator(t *testing.T) {
	fixture := os.Getenv("PADES_TIER2_FIXTURE")
	if fixture == "" {
		fixture = filepath.Join("testdata", "tier2_blt_sample.pdf")
	}
	pdf, err := os.ReadFile(fixture)
	if err != nil {
		t.Skipf("no fixture at %s: %v (run dev-signer to regenerate)", fixture, err)
	}

	body, err := json.Marshal(map[string]any{
		"signedDocument": map[string]any{
			"bytes":    base64.StdEncoding.EncodeToString(pdf),
			"name":     filepath.Base(fixture),
			"mimeType": map[string]string{"mimeTypeString": "application/pdf"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", euDSSValidator, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("eu dss POST: %v (is ec.europa.eu reachable from this runner?)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("eu dss status=%d", resp.StatusCode)
	}

	var report struct {
		SimpleReport struct {
			Signature []struct {
				Indication string `json:"Indication"`
			} `json:"Signature"`
		} `json:"SimpleReport"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode validation report: %v", err)
	}
	if len(report.SimpleReport.Signature) == 0 {
		t.Fatal("validation report has no signatures — fixture may be unsigned")
	}
	for i, sig := range report.SimpleReport.Signature {
		if !strings.EqualFold(sig.Indication, "TOTAL_PASSED") {
			t.Errorf("signature[%d] indication=%q, want TOTAL_PASSED", i, sig.Indication)
		}
	}
}
