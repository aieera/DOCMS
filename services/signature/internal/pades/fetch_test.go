package pades

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/netguard"
)

// The OCSP responder and CRL distribution point URLs come from the
// certificate inside an uploaded PDF, so they are attacker-chosen. With the
// default client (no HTTPClient configured — what VerifyPDF uses) a URL that
// points at a private/loopback address must never be dialed (H-3).
func TestOCSPFetch_PrivateAddressRefused(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	fc := newFixtureChain(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	leaf := *fc.leafCert
	leaf.OCSPServer = []string{srv.URL + "/ocsp"}
	leaf.CRLDistributionPoints = []string{srv.URL + "/crl"}

	if _, err := fetchOCSP(context.Background(), httpOr(nil), &leaf, fc.issuerCert); err == nil {
		t.Fatal("fetchOCSP against a loopback responder must fail")
	}
	if _, err := fetchCRL(context.Background(), httpOr(nil), &leaf, fc.issuerCert); err == nil {
		t.Fatal("fetchCRL against a loopback distribution point must fail")
	}
	// The verifier's live-lookup path with no configured client.
	_ = resolveCertStatus(context.Background(), &leaf, []*x509.Certificate{fc.issuerCert, &leaf}, time.Now(), nil, false)

	if n := hits.Load(); n != 0 {
		t.Fatalf("the loopback server was reached %d times; private destinations must be refused at dial time", n)
	}
}

// A CRL distribution point that streams an oversized body must be cut off
// at the cap rather than buffered whole.
func TestCRLFetch_BodyCapped(t *testing.T) {
	fc := newFixtureChain(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	// A genuine CRL signed by the issuer, padded with revoked entries so it
	// is larger than the (test-lowered) cap.
	tmpl := &x509.RevocationList{
		Number:     big.NewInt(1),
		ThisUpdate: time.Now().Add(-time.Minute),
		NextUpdate: time.Now().Add(time.Hour),
	}
	for i := 0; i < 64; i++ {
		tmpl.RevokedCertificateEntries = append(tmpl.RevokedCertificateEntries, x509.RevocationListEntry{
			SerialNumber: big.NewInt(int64(1000 + i)), RevocationTime: time.Now().Add(-time.Hour),
		})
	}
	crlDER, err := x509.CreateRevocationList(rand.Reader, tmpl, fc.issuerCert, fc.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(crlDER)
	}))
	defer srv.Close()

	orig := maxCRLBytes
	maxCRLBytes = int64(len(crlDER) / 2)
	defer func() { maxCRLBytes = orig }()

	leaf := *fc.leafCert
	leaf.CRLDistributionPoints = []string{srv.URL + "/crl"}
	hc := netguard.NewClient(netguard.Options{AllowPrivate: true}) // reach httptest; the cap is under test
	_, err = fetchCRL(context.Background(), hc, &leaf, fc.issuerCert)
	if !errors.Is(err, netguard.ErrBodyTooLarge) {
		t.Fatalf("fetchCRL err = %v, want netguard.ErrBodyTooLarge", err)
	}
}
