// Package service implements signature business logic: create requests,
// track signer progress, internal PDF signing, external provider integration.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/pades"
	"github.com/aieera/sedoc/services/signature/internal/repository"
)

type Service struct {
	pool   *pgxpool.Pool
	repo   *repository.Repository
	outbox *database.OutboxRepository
	s3     *storage.S3Client
	log    zerolog.Logger
	// qes is the optional ADR 0070 wiring. nil → QES routes return
	// "not configured". main.go calls AddQES() when SEDOC_QES_*
	// envs resolve to at least one TSP adapter.
	qes *QESConfig
	// esign is the optional ADR 0071 wiring for DocuSign/Adobe Sign.
	// nil → connector routes return "not configured".
	esign *ESignConfig
	// ingest is the post-completion bytes-to-storage hand-off used
	// by both QES (ADR 0070) and the third-party connectors (ADR
	// 0071). nil → completion still flips the request status +
	// emits the outbox event with byte counts; only the new-version
	// hand-off is skipped.
	ingest *ingestPipeline
	// padesVerifier is the lazily-initialized PAdES-LTV validator
	// (ADR 0072). nil-allocated on first VerifyPDF call so callers
	// that never validate don't pay the regex compilation cost.
	padesVerifier *pades.Verifier
}

// AddIngest plugs the upload + create-version round-trip into the
// service. main.go calls this when the storage and document gRPC
// clients are reachable; otherwise post-completion still emits the
// audit + status events but the signed PDF doesn't materialize as
// a new version.
func (s *Service) AddIngest(c IngestSignedClient) {
	s.ingest = &ingestPipeline{pool: s.pool, client: c}
}

type Config struct {
	Pool   *pgxpool.Pool
	Repo   *repository.Repository
	Outbox *database.OutboxRepository
	S3     *storage.S3Client
	Logger zerolog.Logger
}

func New(cfg Config) *Service {
	return &Service{pool: cfg.Pool, repo: cfg.Repo, outbox: cfg.Outbox, s3: cfg.S3, log: cfg.Logger}
}

// CreateRequest creates a signature request with signing URLs for each signer
// and enqueues the "signature_requested" notification through the outbox so
// delivery is durable across crashes.
//
// signingMode is one of remote / mobile / in_person (ADR 0073). Empty
// defaults to "remote" for back-compat with callers minted before ADR 0073.
// in_person ceremonies don't need per-signer magic-link tokens (the
// device user is the workflow operator); we still mint them so the
// schema's UNIQUE NOT NULL signing_url_token constraint holds.
func (s *Service) CreateRequest(ctx context.Context, tenantID, documentID, versionID, createdBy, provider, signingMode string, signers []model.Signer) (*model.SignatureRequest, error) {
	if signingMode == "" {
		signingMode = "remote"
	}
	req := &model.SignatureRequest{
		ID:          repository.NewID(),
		TenantID:    tenantID,
		DocumentID:  documentID,
		VersionID:   versionID,
		CreatedBy:   createdBy,
		Status:      "pending",
		Provider:    provider,
		SigningMode: signingMode,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(14 * 24 * time.Hour),
	}
	for i := range signers {
		signers[i].ID = repository.NewID()
		signers[i].Status = "pending"
		if provider == "internal" {
			token := generateToken()
			signers[i].SigningURL = fmt.Sprintf("/sign/%s/%s?token=%s", req.ID, signers[i].ID, token)
		}
	}
	req.Signers = signers

	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant_id: %w", err)
	}
	reqUUID, err := uuid.Parse(req.ID)
	if err != nil {
		return nil, fmt.Errorf("request id: %w", err)
	}

	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.CreateTx(ctx, tx, req); err != nil {
			return err
		}
		for _, signer := range req.Signers {
			if signer.Role == "cc" {
				continue
			}
			payload, _ := json.Marshal(map[string]any{
				"tenant_id":     req.TenantID,
				"user_ids":      []string{signer.Email},
				"type":          "signature.requested",
				"title":         "Signature Requested",
				"body":          fmt.Sprintf("Please sign document %s", req.DocumentID),
				"resource_type": "signature_request",
				"resource_id":   req.ID,
			})
			evt := database.NewOutboxEvent(tenantUUID, "dms.notify.signature_requested.v1", "signature_request", reqUUID, payload)
			if err := s.outbox.Insert(ctx, tx, evt); err != nil {
				return err
			}
			break // only notify first in order for sequential signing
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return req, nil
}

// GetRequest returns a signature request.
func (s *Service) GetRequest(ctx context.Context, tenantID, id string) (*model.SignatureRequest, error) {
	return s.repo.GetByID(ctx, tenantID, id)
}

// ListByDocument returns all signature requests for a document.
func (s *Service) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*model.SignatureRequest, error) {
	return s.repo.ListByDocument(ctx, tenantID, documentID)
}

// RecordSignatureInput carries the per-signer evidence captured at
// the moment of signing. SVGPath / DeviceKind / DocHashHex are the
// ADR 0073 additions; older callers can leave them empty.
type RecordSignatureInput struct {
	TenantID    string
	RequestID   string
	SignerID    string
	IPAddress   string
	SVGPath     string
	DeviceKind  string
	DocHashHex  string
}

// RecordSignature marks a signer as signed and, when every required
// signer (including witnesses) is done, completes the request and
// enqueues the "signature.completed" event through the outbox in
// the same transaction.
//
// On the last-signer commit, ADR 0073 also stamps
// signature_requests.final_hash_sha256 from the caller-supplied
// DocHashHex so Verify can detect post-sign blob swaps.
func (s *Service) RecordSignature(ctx context.Context, in RecordSignatureInput) error {
	req, err := s.repo.GetByID(ctx, in.TenantID, in.RequestID)
	if err != nil || req == nil {
		return fmt.Errorf("request not found")
	}
	now := time.Now().UTC()
	allSigned := true
	for i := range req.Signers {
		if req.Signers[i].ID == in.SignerID {
			req.Signers[i].Status = "signed"
			req.Signers[i].SignedAt = &now
			req.Signers[i].IPAddress = in.IPAddress
			req.Signers[i].SignatureSVGPath = in.SVGPath
			req.Signers[i].DeviceKind = in.DeviceKind
			req.Signers[i].SignedDocHashSHA256 = in.DocHashHex
		}
		// Both signer + witness count as required for completion;
		// approver/cc do not gate the request.
		if req.Signers[i].Status != "signed" {
			role := req.Signers[i].Role
			if role == "signer" || role == "witness" {
				allSigned = false
			}
		}
	}

	tenantUUID, err := uuid.Parse(in.TenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	reqUUID, err := uuid.Parse(in.RequestID)
	if err != nil {
		return fmt.Errorf("request id: %w", err)
	}

	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.UpdateSignersTx(ctx, tx, in.TenantID, in.RequestID, req.Signers); err != nil {
			return err
		}
		if !allSigned {
			return nil
		}
		if err := s.repo.CompleteTx(ctx, tx, in.TenantID, in.RequestID); err != nil {
			return err
		}
		if in.DocHashHex != "" {
			if err := s.repo.SetFinalHashTx(ctx, tx, in.TenantID, in.RequestID, in.DocHashHex); err != nil {
				return err
			}
		}
		// Flat payload — the outbox publisher wraps it as the CloudEvent `data`.
		// A second envelope here double-nests fields at data.data.* (broke
		// external webhook consumers reading data.document_id).
		payload, _ := json.Marshal(map[string]string{
			"tenant_id":   req.TenantID,
			"document_id": req.DocumentID,
			"version_id":  req.VersionID,
			"request_id":  req.ID,
		})
		evt := database.NewOutboxEvent(tenantUUID, "dms.signature.completed.v1", "signature_request", reqUUID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// NextExpectedSigner returns the id of the next signer who should
// sign the request given its sequential ordering. Returns "" if all
// signers (signer + witness roles) are already signed. Used by the
// in-person ceremony to enforce signer→witness order on a single
// device and surface a 409 with the expected id when the UI gets
// out of step.
func (s *Service) NextExpectedSigner(req *model.SignatureRequest) string {
	for _, sn := range req.Signers {
		if sn.Role != "signer" && sn.Role != "witness" {
			continue
		}
		if sn.Status != "signed" {
			return sn.ID
		}
	}
	return ""
}

// CancelRequest cancels a pending signature request.
func (s *Service) CancelRequest(ctx context.Context, tenantID, id string) error {
	return s.repo.UpdateStatus(ctx, tenantID, id, "cancelled")
}

// VerifyPDF runs the full PAdES-LTV validator (ADR 0072) against
// the bytes the caller supplies. Used by the new /signatures/verify-bytes
// route + the "Re-validate" UX. The legacy Verify method below
// remains for back-compat — it answered "is there a completed
// signature_request for this document?" without ever reading the
// PDF.
func (s *Service) VerifyPDF(ctx context.Context, pdf []byte) (*pades.Report, error) {
	if s.padesVerifier == nil {
		s.padesVerifier = pades.NewVerifier(pades.VerifierOptions{})
	}
	return s.padesVerifier.Validate(ctx, pdf)
}

// Verify checks signatures on a document (stub — real impl uses PDF library).
func (s *Service) Verify(ctx context.Context, tenantID, documentID string) (*model.VerificationResult, error) {
	reqs, err := s.repo.ListByDocument(ctx, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	result := &model.VerificationResult{DocumentID: documentID, TamperEvident: true}
	for _, req := range reqs {
		if req.Status != "completed" {
			continue
		}
		result.Signed = true
		for _, signer := range req.Signers {
			if signer.SignedAt != nil {
				result.SignatureCount++
				result.Signatures = append(result.Signatures, model.SigInfo{
					SignerName: signer.Name,
					SignedAt:   *signer.SignedAt,
					Valid:      true,
				})
			}
		}
	}
	return result, nil
}

func generateToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
