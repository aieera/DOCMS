package pades

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Tier-1 contract tests for the PAdES validator. Tier-2 (full
// signing flow against a real DSS sidecar + Adobe Reader) is
// covered by docs/runbooks/pades-ltv-tier2.md.

func TestValidate_RejectsNonPDF(t *testing.T) {
	v := NewVerifier(VerifierOptions{SkipNetworkLookups: true})
	rep, err := v.Validate(context.Background(), []byte("not a pdf"))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if rep == nil {
		t.Fatal("rep should be non-nil even on parse error")
	}
	if !contains(rep.Errors, "parse_error") {
		t.Fatalf("expected parse_error code, got %v", rep.Errors)
	}
}

func TestValidate_FlagsEncryptedPDF(t *testing.T) {
	// Minimal-ish PDF containing /Encrypt in trailer. Doesn't have
	// to be a working encryption — the parser just needs to spot
	// the marker and bail with a structured code.
	pdf := []byte("%PDF-1.7\n1 0 obj<<>>endobj\nxref\n0 1\n0000000000 65535 f \n" +
		"trailer<</Size 1 /Encrypt 2 0 R>>\nstartxref\n9\n%%EOF\n")
	v := NewVerifier(VerifierOptions{SkipNetworkLookups: true})
	rep, err := v.Validate(context.Background(), pdf)
	if !errors.Is(err, ErrUnsupportedEncrypted) {
		t.Fatalf("expected ErrUnsupportedEncrypted, got %v", err)
	}
	if !contains(rep.Errors, "unsupported_encrypted") {
		t.Fatalf("expected unsupported_encrypted code, got %v", rep.Errors)
	}
}

func TestValidate_NoSignatures(t *testing.T) {
	pdf := []byte("%PDF-1.7\n1 0 obj<<>>endobj\nxref\n0 1\n0000000000 65535 f \n" +
		"trailer<</Size 1>>\nstartxref\n9\n%%EOF\n")
	v := NewVerifier(VerifierOptions{SkipNetworkLookups: true})
	rep, err := v.Validate(context.Background(), pdf)
	if !errors.Is(err, ErrNoSignatures) {
		t.Fatalf("expected ErrNoSignatures, got %v", err)
	}
	if rep.SignatureCount != 0 {
		t.Fatalf("count = %d", rep.SignatureCount)
	}
	if !contains(rep.Errors, "no_signatures") {
		t.Fatalf("expected no_signatures, got %v", rep.Errors)
	}
}

func TestParser_LocatesSigDict(t *testing.T) {
	// Hand-crafted PDF with a /Sig dict. The contents are bogus
	// (we'll fail CMS verification later) but the parser should
	// still extract ByteRange + Contents cleanly.
	pdf := buildSignedPDFFixture(t)
	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Signatures) != 1 {
		t.Fatalf("signatures = %d, want 1", len(doc.Signatures))
	}
	sig := doc.Signatures[0]
	if sig.ByteRange[0] != 0 {
		t.Fatalf("byte range start = %d", sig.ByteRange[0])
	}
	if sig.Reason != "test signature" {
		t.Fatalf("reason = %q", sig.Reason)
	}
	if len(sig.Contents) == 0 {
		t.Fatal("contents empty")
	}
}

func TestParser_FullCoverageReportsTamper(t *testing.T) {
	pdf := buildSignedPDFFixture(t)
	// Append bytes after %%EOF to simulate post-sign tamper.
	tampered := append(append([]byte{}, pdf...), []byte("\nTAMPERED\n")...)

	v := NewVerifier(VerifierOptions{SkipNetworkLookups: true})
	rep, _ := v.Validate(context.Background(), tampered)
	if rep == nil || len(rep.Signatures) == 0 {
		t.Fatal("expected at least one signature in report")
	}
	// Either the document-level TamperEvident is false, or the
	// signature-level "bytes_after_signature" code is present.
	if rep.TamperEvident && !contains(rep.Signatures[0].Errors, "bytes_after_signature") {
		t.Fatalf("expected tamper signal; rep=%+v", rep)
	}
}

func TestInferLevel_BB(t *testing.T) {
	// PDF with no /DSS and no /DocTimeStamp → B-B.
	pdf := buildSignedPDFFixture(t)
	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := inferLevel(doc, doc.Signatures[0])
	if got != LevelBB {
		t.Fatalf("level = %q, want %q", got, LevelBB)
	}
}

func TestInferLevel_BLT_WhenDSSPresent(t *testing.T) {
	// Append a minimal /DSS with at least one OCSP entry — enough
	// for inferLevel to upgrade.
	pdf := buildSignedPDFFixture(t)
	pdf = append(pdf, []byte("\n5 0 obj\n<</Type /DSS /OCSPs [9 0 R]>>\nendobj\n")...)
	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.DSS == nil {
		t.Fatal("DSS not detected")
	}
	got := inferLevel(doc, doc.Signatures[0])
	if got != LevelBLT {
		t.Fatalf("level = %q, want B-LT", got)
	}
}

func TestVRIKey_StableSHA1Hex(t *testing.T) {
	a := vriKey([]byte("hello"))
	b := vriKey([]byte("hello"))
	if a != b {
		t.Fatalf("vriKey not stable: %s vs %s", a, b)
	}
	if len(a) != 40 {
		t.Fatalf("vriKey length = %d, want 40 (SHA-1 hex)", len(a))
	}
}

func TestEmbedDSS_AppendsToOriginal(t *testing.T) {
	original := buildSignedPDFFixture(t)
	mat := dssMaterial{
		SignatureContents: []byte("\xde\xad\xbe\xef"),
		CertChain:         [][]byte{[]byte("cert-1-DER")},
		OCSPResponses:     [][]byte{[]byte("ocsp-1-DER")},
	}
	out, err := embedDSS(original, []dssMaterial{mat})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(out) <= len(original) {
		t.Fatalf("output not longer than original: %d vs %d", len(out), len(original))
	}
	// Original prefix preserved — critical, otherwise the existing
	// signature's ByteRange would no longer align.
	if string(out[:len(original)]) != string(original) {
		t.Fatal("original bytes mutated by embedDSS")
	}
	// /DSS dict should appear in the suffix.
	if !strings.Contains(string(out[len(original):]), "/Type /DSS") {
		t.Fatalf("appended bytes missing /DSS dict; suffix=%q", out[len(original):])
	}
	// /VRI key should match the SHA-1 of the contents we passed.
	wantKey := vriKey(mat.SignatureContents)
	if !strings.Contains(string(out), wantKey) {
		t.Fatalf("VRI key %q not found in output", wantKey)
	}
}

func TestNowOr_ZeroFallsBackToNow(t *testing.T) {
	got := nowOr(time.Time{})
	if time.Since(got) > time.Second {
		t.Fatalf("nowOr(zero) drifted from real now: %v", got)
	}
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !nowOr(fixed).Equal(fixed) {
		t.Fatal("nowOr(non-zero) didn't return its input")
	}
}

// ----- fixture helpers --------------------------------------------

// buildSignedPDFFixture writes a tiny PDF with one /Sig dict that
// has a real /ByteRange + /Contents shape. The CMS bytes inside
// /Contents are dummy (zeros) — sufficient for parser tests; CMS
// verification is exercised separately via the real-fixture tests
// that read sample PDFs from testdata/ when present.
func buildSignedPDFFixture(t *testing.T) []byte {
	t.Helper()
	// Build a doc with a /Sig where Contents covers offsets [80..200]
	// (a 120-byte zeroed gap) and signed bytes are everything else.
	const contentsHexLen = 240 // 120 bytes hex-encoded
	// Non-zero hex so the PAdES "trim trailing zeros" step doesn't
	// erase the whole field. Real CMS bytes never end in many
	// zeros; this matches the realistic shape.
	hexZeros := strings.Repeat("ab", contentsHexLen/2)
	body := "%PDF-1.7\n" +
		"1 0 obj <</Type /Catalog /Root 1 0 R>> endobj\n" +
		"2 0 obj <</Type /Sig /SubFilter /adbe.pkcs7.detached " +
		"/Reason (test signature) /Location (test) /Name (Tester) " +
		"/ByteRange [0 80 200 100] " +
		"/Contents <" + hexZeros + ">>> endobj\n" +
		"xref\n0 3\n0000000000 65535 f \n0000000010 00000 n \n0000000060 00000 n \n" +
		"trailer <</Size 3 /Root 1 0 R>>\nstartxref\n400\n%%EOF\n"
	// We need exactly 80 bytes before the contents gap and 100 bytes
	// after, regardless of whether the body is exactly that long.
	// For the parser-shape tests this approximation is fine: it just
	// asserts the regex caught ByteRange + Contents; the verifier
	// tests that need exact offsets either skip CMS (CMS_invalid in
	// errors) or use a real signed-PDF fixture from testdata/.
	return []byte(body)
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
		if strings.HasPrefix(v, s+":") || strings.HasPrefix(v, s) {
			return true
		}
	}
	return false
}
