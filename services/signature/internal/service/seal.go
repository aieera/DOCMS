// Server-seal pipeline (ADR 0025 / Wave 9.2b).
//
// SealVersion takes an existing document version, fetches its decrypted PDF
// bytes from the document service, applies an organizational PAdES seal via
// the configured Signer (the DSS sidecar in prod), and ingests the result as
// a new version. It is the bridge that finally makes the signer produce a
// durable signed artifact — invoked both by the internal seal endpoint and
// (Wave 9.2b cont.) the workflow signature-completed consumer.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aieera/sedoc/services/signature/internal/signer"
)

// maxSealPDF caps the bytes we'll pull from the decrypt-stream — a guard
// against a malformed/huge response wedging the sealer.
const maxSealPDF = 200 << 20 // 200 MB

// internalServiceKeyHeader mirrors pkg/middleware.InternalServiceKeyHeader;
// duplicated here to avoid the service layer depending on the HTTP middleware.
const internalServiceKeyHeader = "X-Internal-Service-Key"

// Sealer holds the server-seal dependencies. Constructed by AddSealer.
type Sealer struct {
	signer        signer.Signer
	ingest        *ingestPipeline
	docHTTPBase   string // e.g. http://document:8080
	internalKey   string // SEDOC_INTERNAL_API_KEY
	gatewaySecret string // SEDOC_GATEWAY_SECRET — satisfies the document service's gateway-sig check on the direct (non-gateway) fetch
	httpClient    *http.Client
}

// AddSealer wires the server-seal pipeline. main.go calls this when a Signer
// is configured (SEDOC_SIGNER) and the document HTTP base + internal key are
// present; otherwise SealVersion returns "not configured".
func (s *Service) AddSealer(sgnr signer.Signer, docHTTPBase, internalKey, gatewaySecret string) {
	s.sealer = &Sealer{
		signer:        sgnr,
		ingest:        s.ingest,
		docHTTPBase:   docHTTPBase,
		internalKey:   internalKey,
		gatewaySecret: gatewaySecret,
		httpClient:    &http.Client{Timeout: 60 * time.Second},
	}
}

// SealResult is what SealVersion returns.
type SealResult struct {
	NewVersionID string
	Level        string
	Fingerprint  string
}

// SealVersion fetches (documentID, versionID)'s decrypted PDF, server-seals it,
// and ingests the sealed bytes as a new version. signerName/reason populate the
// PAdES signerInfo. Returns the new version id.
func (s *Service) SealVersion(ctx context.Context, tenantID, documentID, versionID, userID, signerName, reason string) (*SealResult, error) {
	if s.sealer == nil || s.sealer.signer == nil {
		return nil, errors.New("seal: signer not configured")
	}
	if s.sealer.ingest == nil {
		return nil, errors.New("seal: ingest pipeline not configured")
	}
	if signerName == "" {
		signerName = "SeDoc Organizational Seal"
	}

	pdf, err := s.sealer.fetchVersionPDF(ctx, tenantID, documentID, versionID)
	if err != nil {
		return nil, fmt.Errorf("seal fetch: %w", err)
	}

	resp, err := s.sealer.signer.Sign(ctx, signer.Request{
		PDFBytes:   pdf,
		SignerName: signerName,
		Reason:     reason,
		Mode:       signer.ModeServerHSM,
		// The DSS sidecar signs with its configured server key; KMSAlias is
		// required by validateCommon and (in KMS-backed deployments) selects
		// the per-tenant signing key.
		KMSAlias: "tenant-seal:" + tenantID,
		Level:    signer.LevelBLT, // sidecar downgrades to B-B when no TSA
	})
	if err != nil {
		return nil, fmt.Errorf("seal sign: %w", err)
	}

	newVer, _, err := s.sealer.ingest.PutAndCreateVersion(ctx, PutSignedBlobInput{
		TenantID:   tenantID,
		UserID:     userID,
		Filename:   "sealed.pdf",
		MimeType:   "application/pdf",
		Bytes:      resp.PDFBytes,
		DocumentID: documentID,
	}, documentID, userID, "Server seal ("+string(resp.Level)+")")
	if err != nil {
		return nil, fmt.Errorf("seal ingest: %w", err)
	}

	return &SealResult{NewVersionID: newVer, Level: string(resp.Level), Fingerprint: resp.Fingerprint}, nil
}

// fetchVersionPDF GETs the document service's decrypt-stream endpoint as a
// trusted internal service and returns the decrypted bytes.
func (sl *Sealer) fetchVersionPDF(ctx context.Context, tenantID, documentID, versionID string) ([]byte, error) {
	url := fmt.Sprintf("%s/api/v1/documents/%s/versions/%s/decrypt-stream",
		sl.docHTTPBase, documentID, versionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(internalServiceKeyHeader, sl.internalKey)
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	if sl.gatewaySecret != "" {
		// The decrypt-stream endpoint sits behind RequireGatewaySignature; on a
		// direct service-to-service call we present the shared secret ourselves.
		req.Header.Set("X-Gateway-Signature", sl.gatewaySecret)
	}

	resp, err := sl.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return nil, fmt.Errorf("decrypt-stream %s: status %d: %s", versionID, resp.StatusCode, string(body))
	}
	pdf, err := io.ReadAll(io.LimitReader(resp.Body, maxSealPDF))
	if err != nil {
		return nil, err
	}
	if len(pdf) == 0 {
		return nil, errors.New("decrypt-stream returned no bytes")
	}
	return pdf, nil
}
