package pades

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ocsp"
)

// ----- extractStream ----------------------------------------------

func TestExtractStream_FindsBodyBetweenMarkers(t *testing.T) {
	pdf := []byte("%PDF-1.7\n" +
		"1 0 obj\n<<>>\nendobj\n" +
		"7 0 obj\n<< /Length 11 >>\nstream\nHELLO-WORLD\nendstream\nendobj\n" +
		"trailer<</Size 8>>\nstartxref\n0\n%%EOF\n")
	got, err := extractStream(pdf, 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "HELLO-WORLD" {
		t.Fatalf("got=%q", got)
	}
}

func TestExtractStream_MissingObjReturnsEmpty(t *testing.T) {
	pdf := []byte("%PDF-1.7\nxref\n%%EOF\n")
	got, err := extractStream(pdf, 99)
	if err != nil || got != nil {
		t.Fatalf("expected nil/nil, got %q/%v", got, err)
	}
}

// ----- validateEmbeddedLTV — real OCSP fixtures ------------------

// fixtureChain holds a self-signed issuer + a leaf cert it issued.
// Generated fresh per test so we can swap NotBefore/NotAfter to
// exercise the validity-window logic without time-mocking.
type fixtureChain struct {
	issuerCert *x509.Certificate
	issuerKey  *rsa.PrivateKey
	leafCert   *x509.Certificate
	leafKey    *ecdsa.PrivateKey
}

func newFixtureChain(t *testing.T, leafNotBefore, leafNotAfter time.Time) *fixtureChain {
	t.Helper()
	// Self-signed issuer.
	issuerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuerTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "SeDoc Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuerTmpl, issuerTmpl, &issuerKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := x509.ParseCertificate(issuerDER)
	if err != nil {
		t.Fatal(err)
	}
	// Leaf signed by issuer.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "alice@example.com"},
		NotBefore:    leafNotBefore,
		NotAfter:     leafNotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, issuer, &leafKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureChain{issuerCert: issuer, issuerKey: issuerKey, leafCert: leaf, leafKey: leafKey}
}

// mintOCSP signs a real OCSP response for fc.leafCert with the
// supplied status + ProducedAt. Returns the DER bytes Adobe / EU
// validators would parse.
func (fc *fixtureChain) mintOCSP(t *testing.T, status int, producedAt time.Time) []byte {
	t.Helper()
	tmpl := ocsp.Response{
		Status:       status,
		SerialNumber: fc.leafCert.SerialNumber,
		ProducedAt:   producedAt,
		ThisUpdate:   producedAt,
		NextUpdate:   producedAt.Add(24 * time.Hour),
	}
	if status == ocsp.Revoked {
		tmpl.RevokedAt = producedAt.Add(-time.Hour)
		tmpl.RevocationReason = ocsp.KeyCompromise
	}
	der, err := ocsp.CreateResponse(fc.issuerCert, fc.issuerCert, tmpl, fc.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// buildPDFWithEmbeddedOCSP constructs a tiny PDF that has:
//   - one /Sig dict (Contents = `sigContents` in hex)
//   - one stream object containing the supplied OCSP DER
//   - one /DSS dict with /VRI keyed on SHA-1(sigContents) → that stream
//
// Output is what `parsePDF` would walk; close enough to a real
// signed PDF that validateEmbeddedLTV exercises its full logic.
func buildPDFWithEmbeddedOCSP(t *testing.T, sigContents []byte, ocspDER []byte) []byte {
	t.Helper()
	contentsHex := hex.EncodeToString(sigContents)
	vri := vriKey(sigContents)
	streamObj := fmt.Sprintf("9 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		len(ocspDER), string(ocspDER))
	dssObj := fmt.Sprintf("10 0 obj\n<<\n/Type /DSS\n/OCSPs [9 0 R]\n/VRI <</%s <</OCSP [9 0 R]>>>>\n>>\nendobj\n", vri)
	doc := "%PDF-1.7\n" +
		"1 0 obj <</Type /Catalog /Root 1 0 R /DSS 10 0 R>> endobj\n" +
		"2 0 obj <</Type /Sig /SubFilter /adbe.pkcs7.detached " +
		"/Reason (test) /ByteRange [0 80 200 100] " +
		"/Contents <" + contentsHex + ">>> endobj\n" +
		streamObj +
		dssObj +
		"trailer <</Size 11 /Root 1 0 R>>\nstartxref\n400\n%%EOF\n"
	return []byte(doc)
}

func TestValidateEmbeddedLTV_GoodOCSP_LiftsToValid(t *testing.T) {
	// Cert validity: yesterday → 1 hour ago. Cert IS expired.
	// OCSP issued during validity. Without LTV the verdict would
	// be Indeterminate; with LTV it should lift to Valid.
	notBefore := time.Now().Add(-2 * time.Hour)
	notAfter := time.Now().Add(-1 * time.Hour) // expired 1h ago
	fc := newFixtureChain(t, notBefore, notAfter)
	ocspDER := fc.mintOCSP(t, ocsp.Good, time.Now().Add(-90*time.Minute))

	// Build a signature-contents blob. Deterministic so vriKey
	// matches between PDF generation and lookup.
	sigContents := []byte("DEADBEEF-test-signature-blob")
	pdf := buildPDFWithEmbeddedOCSP(t, sigContents, ocspDER)

	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DSS == nil {
		t.Fatal("DSS not parsed")
	}
	sigBlk := signatureBlock{Contents: sigContents}
	status, used := validateEmbeddedLTV(doc, sigBlk, fc.leafCert, []*x509.Certificate{fc.issuerCert, fc.leafCert})
	if !used {
		t.Fatal("validateEmbeddedLTV should have used the embedded OCSP")
	}
	if status != StatusValid {
		t.Fatalf("status=%q, want valid (cert expired but OCSP issued during validity)", status)
	}
}

func TestValidateEmbeddedLTV_RevokedOCSP_ReportsRevoked(t *testing.T) {
	notBefore := time.Now().Add(-2 * time.Hour)
	notAfter := time.Now().Add(-1 * time.Hour)
	fc := newFixtureChain(t, notBefore, notAfter)
	ocspDER := fc.mintOCSP(t, ocsp.Revoked, time.Now().Add(-90*time.Minute))

	sigContents := []byte("DEADBEEF-revoked-test")
	pdf := buildPDFWithEmbeddedOCSP(t, sigContents, ocspDER)

	doc, err := parsePDF(pdf)
	if err != nil {
		t.Fatal(err)
	}
	sigBlk := signatureBlock{Contents: sigContents}
	status, used := validateEmbeddedLTV(doc, sigBlk, fc.leafCert, []*x509.Certificate{fc.issuerCert, fc.leafCert})
	if !used {
		t.Fatal("expected used=true for an embedded revocation answer")
	}
	if status != StatusRevoked {
		t.Fatalf("status=%q, want revoked", status)
	}
}

func TestValidateEmbeddedLTV_OCSPProducedOutsideWindow_FallsThrough(t *testing.T) {
	// Cert validity: a 1-hour window. OCSP produced AFTER cert
	// expired — that's not eIDAS-LTV proof; we must not lift.
	notBefore := time.Now().Add(-3 * time.Hour)
	notAfter := time.Now().Add(-2 * time.Hour)
	fc := newFixtureChain(t, notBefore, notAfter)
	ocspDER := fc.mintOCSP(t, ocsp.Good, time.Now().Add(-30*time.Minute)) // post-expiry

	sigContents := []byte("DEADBEEF-late-ocsp")
	pdf := buildPDFWithEmbeddedOCSP(t, sigContents, ocspDER)
	doc, _ := parsePDF(pdf)
	sigBlk := signatureBlock{Contents: sigContents}
	_, used := validateEmbeddedLTV(doc, sigBlk, fc.leafCert, []*x509.Certificate{fc.issuerCert, fc.leafCert})
	if used {
		t.Fatal("must NOT use OCSP whose ProducedAt falls outside cert validity")
	}
}

func TestValidateEmbeddedLTV_MismatchedSerial_DoesNotMatch(t *testing.T) {
	// Embedded OCSP is for a different cert than the signer. The
	// matcher should ignore it (otherwise we'd assert validity
	// based on a totally unrelated answer).
	fc := newFixtureChain(t, time.Now().Add(-2*time.Hour), time.Now().Add(-1*time.Hour))
	otherCert := &x509.Certificate{SerialNumber: big.NewInt(99999)}
	resp := ocsp.Response{
		Status: ocsp.Good, SerialNumber: otherCert.SerialNumber,
		ProducedAt: time.Now().Add(-90 * time.Minute),
		ThisUpdate: time.Now().Add(-90 * time.Minute),
	}
	ocspDER, err := ocsp.CreateResponse(fc.issuerCert, fc.issuerCert, resp, fc.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	sigContents := []byte("DEADBEEF-mismatched")
	pdf := buildPDFWithEmbeddedOCSP(t, sigContents, ocspDER)
	doc, _ := parsePDF(pdf)
	sigBlk := signatureBlock{Contents: sigContents}
	_, used := validateEmbeddedLTV(doc, sigBlk, fc.leafCert, []*x509.Certificate{fc.issuerCert, fc.leafCert})
	if used {
		t.Fatal("must NOT match an OCSP response for a different serial")
	}
}

func TestValidateEmbeddedLTV_NoDSS_FallsThrough(t *testing.T) {
	fc := newFixtureChain(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	doc := &parsedDoc{Bytes: []byte("%PDF-1.7\n%%EOF\n")}
	_, used := validateEmbeddedLTV(doc, signatureBlock{Contents: []byte("x")}, fc.leafCert, nil)
	if used {
		t.Fatal("no DSS means no embedded answer")
	}
}

// silence unused
var _ = sha256.Sum256
var _ = strings.Contains
