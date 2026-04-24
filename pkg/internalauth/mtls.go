package internalauth

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
)

// hasPeerCert reports whether the request was delivered over TLS with
// at least one peer certificate. Used by ModeBoth to decide whether
// to try mTLS before falling back to HMAC.
func hasPeerCert(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.PeerCertificates) > 0
}

// verifyMTLS validates the TLS peer certificate chain against the
// configured CA and checks the client cert's DNS SAN is in the
// allowlist. Returns a non-nil error (and records an outcome label)
// on failure.
func (v *Verifier) verifyMTLS(r *http.Request) error {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		record("none", "missing")
		return errors.New("missing client certificate")
	}
	leaf := r.TLS.PeerCertificates[0]

	// Build intermediates from any additional peer certs.
	inters := x509.NewCertPool()
	for _, c := range r.TLS.PeerCertificates[1:] {
		inters.AddCert(c)
	}

	opts := x509.VerifyOptions{
		Roots:         v.cfg.CAPool,
		Intermediates: inters,
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		if isExpiredError(err) {
			record("mtls", "bad_cert")
			return fmt.Errorf("client certificate expired: %w", err)
		}
		record("mtls", "bad_cert")
		return fmt.Errorf("client certificate not trusted: %w", err)
	}

	// SAN allowlist: at least one DNS name on the cert must be in
	// the service's allowlist. Empty allowlist means the service has
	// not declared its expected peers and we fail closed — admins
	// cannot accidentally deploy a service that accepts every cert
	// the CA has ever issued.
	if len(v.cfg.SANAllowlist) == 0 {
		record("mtls", "bad_san")
		return errors.New("no SAN allowlist configured")
	}
	for _, san := range leaf.DNSNames {
		for _, allowed := range v.cfg.SANAllowlist {
			if san == allowed {
				return nil
			}
		}
	}
	record("mtls", "bad_san")
	return fmt.Errorf("client SAN not in allowlist (got %v)", leaf.DNSNames)
}

func isExpiredError(err error) bool {
	var inv x509.CertificateInvalidError
	if errors.As(err, &inv) {
		return inv.Reason == x509.Expired
	}
	return false
}
