package pades

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
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
	for _, url := range cert.CRLDistributionPoints {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode >= 400 {
			continue
		}
		// crypto/x509.ParseRevocationList replaced ParseDERCRL in
		// Go 1.19+ — works for both PEM and DER inputs.
		crl, err := x509.ParseRevocationList(body)
		if err != nil {
			continue
		}
		// Only trust a CRL actually signed by the issuer (fail closed to the next
		// CDP / caller fallback otherwise).
		if crl.CheckSignatureFrom(issuer) != nil {
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
	return nil, fmt.Errorf("crl: all distribution points failed")
}
