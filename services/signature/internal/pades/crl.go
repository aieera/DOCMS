package pades

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/netguard"
)

// CRLLookup represents the relevant slice of a CRL — the cert's
// revocation status + the CRL's update window — with the raw DER
// bytes for /DSS embedding.
type CRLLookup struct {
	Status     CertStatus
	ThisUpdate time.Time
	NextUpdate time.Time
	RawCRL     []byte
}

// fetchCRL pulls a CRL from the cert's CRLDistributionPoints,
// parses it, and reports the cert's revocation status. Falls
// through to the next URL on parse failure.
func fetchCRL(ctx context.Context, hc *http.Client, cert, issuer *x509.Certificate) (*CRLLookup, error) {
	if len(cert.CRLDistributionPoints) == 0 {
		return nil, errors.New("crl: no distribution points")
	}
	// A CRL is fetched over an unauthenticated (frequently plain http) CDP, so we
	// MUST verify it is signed by the cert's issuer before trusting it — otherwise
	// a MITM/hijacked CDP can return a forged CRL that omits a revoked serial and
	// a revoked cert reads as good. No issuer → nothing to verify against.
	if issuer == nil {
		return nil, errors.New("crl: no issuer to verify CRL signature")
	}
	lastErr := errors.New("no fetchable distribution point")
	tried := 0
	for _, url := range cert.CRLDistributionPoints {
		if tried == maxRevocationURLs {
			break
		}
		if !fetchableURL(url) {
			continue
		}
		tried++
		body, err := getCRL(ctx, hc, url)
		if err != nil {
			lastErr = err
			continue
		}
		// crypto/x509.ParseRevocationList replaced ParseDERCRL in
		// Go 1.19+ — works for both PEM and DER inputs.
		crl, err := x509.ParseRevocationList(body)
		if err != nil {
			lastErr = err
			continue
		}
		// Only trust a CRL actually signed by the issuer (fail closed to the next
		// CDP / caller fallback otherwise).
		if err := crl.CheckSignatureFrom(issuer); err != nil {
			lastErr = err
			continue
		}
		status := StatusValid
		for _, entry := range crl.RevokedCertificateEntries {
			if entry.SerialNumber != nil && cert.SerialNumber != nil &&
				entry.SerialNumber.Cmp(cert.SerialNumber) == 0 {
				status = StatusRevoked
				break
			}
		}
		return &CRLLookup{
			Status:     status,
			ThisUpdate: crl.ThisUpdate,
			NextUpdate: crl.NextUpdate,
			RawCRL:     body,
		}, nil
	}
	return nil, fmt.Errorf("crl: all distribution points failed: %w", lastErr)
}

// getCRL fetches one distribution point and returns the body of a 2xx
// answer, read under maxCRLBytes.
func getCRL(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("crl distribution point status %d", resp.StatusCode)
	}
	return netguard.LimitedBody(resp.Body, maxCRLBytes)
}

// maxCRLBytes caps a fetched CRL body. Large public CAs publish CRLs of a
// few MB; anything bigger is refused rather than buffered. A var so tests
// can lower it.
var maxCRLBytes int64 = 8 << 20
