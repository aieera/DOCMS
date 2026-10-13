package signer

import (
	"context"
	"os"
	"testing"
	"time"
)

// A minimal valid PDF (catalog → pages → one page).
var sidecarTestPDF = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>endobj\n" +
	"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000101 00000 n \n" +
	"trailer<</Size 4/Root 1 0 R>>\nstartxref\n164\n%%EOF\n")

// TestDSSSidecarSignLive exercises the real DSSSidecarSigner against a
// running sidecar through the package's Signer abstraction (the path
// SEDOC_SIGNER=dss takes in production). Skipped unless SIGNER_ADDR is set:
//
//	SIGNER_ADDR=localhost:6060 go test -run Live ./services/signature/internal/signer/ -v
func TestDSSSidecarSignLive(t *testing.T) {
	addr := os.Getenv("SIGNER_ADDR")
	if addr == "" {
		t.Skip("no SIGNER_ADDR; skipping live DSS sidecar test")
	}
	s := NewDSSSidecarSigner(addr)
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := s.Sign(ctx, Request{
		PDFBytes:   sidecarTestPDF,
		SignerName: "Workflow Seal",
		Reason:     "integration test",
		Level:      LevelBB,
		Mode:       ModeServerHSM,
		KMSAlias:   "dev-seal-key", // satisfies validateCommon; dev sidecar uses its baked keystore
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(resp.PDFBytes) <= len(sidecarTestPDF) {
		t.Fatalf("signed pdf not larger than input: %d <= %d", len(resp.PDFBytes), len(sidecarTestPDF))
	}
	if resp.Fingerprint == "" {
		t.Error("expected a fingerprint")
	}
	t.Logf("signed %d→%d bytes, level=%s", len(sidecarTestPDF), len(resp.PDFBytes), resp.Level)
}
