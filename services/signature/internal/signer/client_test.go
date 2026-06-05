package signer

import (
	"context"
	"os"
	"testing"
	"time"

	signerv1 "github.com/aieera/sedoc/proto/gen/go/signerv1"
)

// A minimal valid PDF (catalog → pages → one page).
var testPDF = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\n" +
	"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000101 00000 n \n" +
	"trailer<</Size 4/Root 1 0 R>>\nstartxref\n164\n%%EOF\n")

// TestSealAndVerifyLive exercises the real Go→sidecar Seal → Verify round-trip
// against a running signer. Skipped unless SIGNER_ADDR is set, e.g.:
//
//	SIGNER_ADDR=localhost:6060 go test -run Live ./services/signature/internal/signer/ -v
func TestSealAndVerifyLive(t *testing.T) {
	addr := os.Getenv("SIGNER_ADDR")
	if addr == "" {
		t.Skip("no SIGNER_ADDR; skipping live signer integration test")
	}
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := c.Seal(ctx, SealRequest{
		PDF:        testPDF,
		SignerName: "Workflow Seal",
		Reason:     "integration test",
		Level:      signerv1.Level_LEVEL_BB,
	})
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if len(res.PDF) <= len(testPDF) {
		t.Fatalf("sealed pdf not larger than input: %d <= %d", len(res.PDF), len(testPDF))
	}
	if res.Fingerprint == "" {
		t.Error("expected a fingerprint")
	}
	t.Logf("sealed %d→%d bytes, level=%s", len(testPDF), len(res.PDF), res.Level)

	vr, err := c.Verify(ctx, res.PDF)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if vr.GetSignatureCount() != 1 {
		t.Errorf("want 1 signature, got %d", vr.GetSignatureCount())
	}
	if !vr.GetTamperEvident() {
		t.Error("want tamper-evident")
	}
	t.Logf("verify: count=%d tamperEvident=%v ltv=%v", vr.GetSignatureCount(), vr.GetTamperEvident(), vr.GetLtvEnabled())
}
