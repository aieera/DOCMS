package pades

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/crypto/ocsp"
)

// OCSPLookup represents one cert's revocation status as fetched
// from an OCSP responder.
type OCSPLookup struct {
	Status     CertStatus
	ProducedAt time.Time
	ThisUpdate time.Time
	NextUpdate time.Time
	// RawResponse is the DER bytes of the OCSP response — embedded
	// verbatim into /DSS for LTV.
	RawResponse []byte
}

// fetchOCSP issues an OCSP request for `cert` against its declared
// responder. Returns ErrNoResponder when the cert doesn't carry a
// responder URL — caller falls back to CRL.
func fetchOCSP(ctx context.Context, hc *http.Client, cert, issuer *x509.Certificate) (*OCSPLookup, error) {
	if len(cert.OCSPServer) == 0 {
		return nil, errOCSPNoResponder
	}
	if issuer == nil {
		return nil, errors.New("ocsp: issuer cert required")
	}
	reqDER, err := ocsp.CreateRequest(cert, issuer, &ocsp.RequestOptions{Hash: cryptoSHA1})
	if err != nil {
		return nil, fmt.Errorf("ocsp request: %w", err)
	}
	for _, url := range cert.OCSPServer {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqDER))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/ocsp-request")
		req.Header.Set("Accept", "application/ocsp-response")
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode >= 400 {
			continue
		}
		ocspResp, err := ocsp.ParseResponse(body, issuer)
		if err != nil {
			continue
		}
		return &OCSPLookup{
			Status:      ocspStatusToCertStatus(ocspResp.Status),
			ProducedAt:  ocspResp.ProducedAt,
			ThisUpdate:  ocspResp.ThisUpdate,
			NextUpdate:  ocspResp.NextUpdate,
			RawResponse: body,
		}, nil
	}
	return nil, errors.New("ocsp: all responders failed")
}

func ocspStatusToCertStatus(s int) CertStatus {
	switch s {
	case ocsp.Good:
		return StatusValid
	case ocsp.Revoked:
		return StatusRevoked
	default:
		return StatusUnknown
	}
}

// errOCSPNoResponder lets callers fall back to CRL lookup without
// matching on string content.
var errOCSPNoResponder = errors.New("ocsp: cert has no responder URL")

// cryptoSHA1 is the OCSP-default hash. Mapped explicitly here so we
// don't pull crypto/sha1 into other files; OCSP responders still
// expect SHA-1 by RFC.
//
//nolint:gosec // SHA-1 is the OCSP CertID default per RFC 6960
const cryptoSHA1 = 3 // crypto.SHA1
