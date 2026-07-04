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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/signer"
)

// maxCeremonySigners caps the number of per-signer PAdES revisions so a
// pathological signer list can't blow up memory / the sidecar with an
// ever-growing PDF.
const maxCeremonySigners = 50

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
	// tsaURL is the RFC3161 timestamp authority. REQUIRED for PAdES-B-LT/LTV —
	// without it the DSS sidecar silently downgrades to B-B (no LTV), so the
	// "valid PAdES-LTV" DoD depends on this being set (SIGNER_TSA_URL).
	tsaURL     string
	httpClient *http.Client
}

// AddSealer wires the server-seal pipeline. main.go calls this when a Signer
// is configured (SEDOC_SIGNER) and the document HTTP base + internal key are
// present; otherwise SealVersion returns "not configured".
func (s *Service) AddSealer(sgnr signer.Signer, docHTTPBase, internalKey, gatewaySecret, tsaURL string) {
	s.sealer = &Sealer{
		signer:        sgnr,
		ingest:        s.ingest,
		docHTTPBase:   docHTTPBase,
		internalKey:   internalKey,
		gatewaySecret: gatewaySecret,
		tsaURL:        tsaURL,
		httpClient:    &http.Client{Timeout: 90 * time.Second},
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
		TSAURL:   s.sealer.tsaURL, // enables B-LT + embedded LTV material
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

	// Signed-version lineage is now persisted (PutAndCreateVersion also fires
	// dms.version.uploaded.v1). Emit the signature-specific dms.signature.applied.v1
	// so signature consumers learn a PAdES seal was applied to produce newVer.
	s.emitSignatureApplied(ctx, tenantID, documentID, newVer, userID, signerName, reason, string(resp.Level), resp.Fingerprint)

	return &SealResult{NewVersionID: newVer, Level: string(resp.Level), Fingerprint: resp.Fingerprint}, nil
}

// CeremonySigner is one signer's attribution for a per-signer PAdES revision.
type CeremonySigner struct {
	Name      string
	Email     string
	Reason    string
	FieldName string
}

// SealCeremony materialises a completed signing ceremony as REAL cryptographic
// PAdES revisions: it fetches the original PDF, applies one incremental
// PAdES-B-LT revision per signer (attributed via SignerName/reason/field), then
// a final organizational seal, and ingests the result as a single new version.
// So the final PDF carries N+1 valid PAdES-LTV signatures (with a TSA). In
// ModeServerHSM every revision is signed by the tenant/org key and cryptographically
// ATTRIBUTED to the human signer — the SaaS model; true per-signer certificates
// are the QES/TSP path. Falls back to a single org seal when signers is empty.
func (s *Service) SealCeremony(ctx context.Context, tenantID, documentID, versionID, userID string, signers []CeremonySigner) (*SealResult, error) {
	if s.sealer == nil || s.sealer.signer == nil {
		return nil, errors.New("seal: signer not configured")
	}
	if s.sealer.ingest == nil {
		return nil, errors.New("seal: ingest pipeline not configured")
	}
	if len(signers) == 0 {
		return s.SealVersion(ctx, tenantID, documentID, versionID, userID, "SeDoc Organizational Seal", "Envelope completion seal")
	}

	pdf, err := s.sealer.fetchVersionPDF(ctx, tenantID, documentID, versionID)
	if err != nil {
		return nil, fmt.Errorf("seal fetch: %w", err)
	}

	pdf, level, fingerprint, err := applyCeremonyRevisions(ctx, s.sealer.signer, tenantID, s.sealer.tsaURL, pdf, signers)
	if err != nil {
		return nil, err
	}

	newVer, _, err := s.sealer.ingest.PutAndCreateVersion(ctx, PutSignedBlobInput{
		TenantID:   tenantID,
		UserID:     userID,
		Filename:   "signed.pdf",
		MimeType:   "application/pdf",
		Bytes:      pdf,
		DocumentID: documentID,
	}, documentID, userID, fmt.Sprintf("Signing ceremony (%d signers, %s)", len(signers), level))
	if err != nil {
		return nil, fmt.Errorf("ceremony ingest: %w", err)
	}
	s.emitSignatureApplied(ctx, tenantID, documentID, newVer, userID, "SeDoc signing ceremony", "ceremony completion", level, fingerprint)
	return &SealResult{NewVersionID: newVer, Level: level, Fingerprint: fingerprint}, nil
}

// applyCeremonyRevisions signs one incremental PAdES-B-LT revision per signer
// (attributed) then a final organizational seal, chaining each Sign's output PDF
// into the next input. Pure w.r.t. I/O (takes the signer + initial bytes),
// so the ordering + chaining is unit-testable with a fake signer. Returns the
// final PDF, the achieved level, and the last fingerprint.
func applyCeremonyRevisions(ctx context.Context, sgnr signer.Signer, tenantID, tsaURL string, pdf []byte, signers []CeremonySigner) ([]byte, string, string, error) {
	if len(signers) > maxCeremonySigners {
		return nil, "", "", fmt.Errorf("too many signers (%d > %d)", len(signers), maxCeremonySigners)
	}
	var level, fingerprint string
	sign := func(name, email, reason, field string) error {
		// Each incremental revision grows the PDF; re-check the ceiling so a
		// long ceremony can't grow the in-memory doc past the fetch cap.
		if len(pdf) > maxSealPDF {
			return fmt.Errorf("signed PDF exceeds %d bytes after prior revisions", maxSealPDF)
		}
		resp, err := sgnr.Sign(ctx, signer.Request{
			PDFBytes:    pdf, // the evolving document — each Sign appends an incremental revision
			SignerName:  name,
			SignerEmail: email,
			Reason:      reason,
			Mode:        signer.ModeServerHSM,
			KMSAlias:    "tenant-seal:" + tenantID,
			Level:       signer.LevelBLT,
			TSAURL:      tsaURL,
			FieldName:   field,
		})
		if err != nil {
			return err
		}
		pdf = resp.PDFBytes
		level = string(resp.Level)
		fingerprint = resp.Fingerprint
		return nil
	}
	for i, cs := range signers {
		field := cs.FieldName
		if field == "" {
			field = fmt.Sprintf("Signature_%d", i+1)
		}
		reason := cs.Reason
		if reason == "" {
			reason = "Signed via SeDoc signing ceremony"
		}
		if err := sign(cs.Name, cs.Email, reason, field); err != nil {
			return nil, "", "", fmt.Errorf("per-signer seal (%s): %w", cs.Name, err)
		}
	}
	// Final organizational seal over the fully-signed envelope.
	if err := sign("SeDoc Organizational Seal", "", "Envelope completion seal", "OrgSeal"); err != nil {
		return nil, "", "", fmt.Errorf("org seal: %w", err)
	}
	return pdf, level, fingerprint, nil
}

// ClaimSeal atomically claims the seal for a request (idempotency guard against
// NATS redelivery). Returns true when the caller won the claim.
func (s *Service) ClaimSeal(ctx context.Context, tenantID, requestID string) (bool, error) {
	if requestID == "" {
		return true, nil // no request id (e.g. org-only path) — nothing to dedup on
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return false, err
	}
	var claimed bool
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		claimed, err = s.repo.ClaimSealTx(ctx, tx, tenantID, requestID)
		return err
	})
	return claimed, err
}

// ReleaseSeal clears the claim so a failed seal can retry on redelivery.
func (s *Service) ReleaseSeal(ctx context.Context, tenantID, requestID string) {
	if requestID == "" {
		return
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return
	}
	if err := database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return s.repo.ReleaseSealTx(ctx, tx, tenantID, requestID)
	}); err != nil {
		// A failed release means the claim stays held and redeliveries
		// will Ack-skip: the seal is silently dropped. Loud log so an
		// operator can re-trigger it; a claimed_at-with-expiry claim
		// (instead of the terminal sealed_at doubling as the claim) is
		// the durable fix and is tracked as a follow-up.
		s.log.Error().Err(err).Str("request_id", requestID).
			Msg("seal release FAILED — claim still held; seal will not retry without manual intervention")
	}
}

// CeremonySigners loads the ordered, non-cc signers of a request for
// per-signer sealing (name/email/reason/field). Only actual signers + witnesses
// materialise as PAdES revisions; cc/approver roles don't.
func (s *Service) CeremonySigners(ctx context.Context, tenantID, requestID string) ([]CeremonySigner, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	// Read inside a tenant tx so app.current_tenant is set — the seal consumer
	// runs off a NATS message with no HTTP tenant GUC, and signature_requests is
	// RLS-forced, so a raw pool read would silently return 0 rows and drop us to
	// an org-only seal.
	var req *model.SignatureRequest
	if err := database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		r, e := s.repo.GetByIDTx(ctx, tx, tenantID, requestID)
		req = r
		return e
	}); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, nil
	}
	out := make([]CeremonySigner, 0, len(req.Signers))
	for _, sg := range req.Signers {
		if sg.Role == "cc" || sg.Role == "approver" {
			continue
		}
		out = append(out, CeremonySigner{
			Name:      sg.Name,
			Email:     sg.Email,
			Reason:    "Signed as " + sg.Role,
			FieldName: "Signature_" + sg.ID,
		})
	}
	return out, nil
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
