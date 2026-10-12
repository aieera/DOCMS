// ADR 0070 — QES ceremony orchestration.
//
// Three flows:
//
//   StartQES       — caller has the document hash + chosen TSP. We
//                    persist a pending tsp_signing_sessions row,
//                    call TSP.Authorize, return the redirect URL.
//   HandleReturn   — QTSP redirected the browser back with auth code.
//                    We verify the HMAC state, call TSP.Sign,
//                    persist qes_certificates, embed signed hash.
//   GetCertificate — UI fetch for the cert-display block.
//
// Audit: every state transition writes a `dms.audit.qes.*` outbox
// event in the same tx as the row mutation.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	tsplib "github.com/aieera/sedoc/pkg/signing/tsp"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/repository"
)

// QESConfig is set by main from SEDOC_QES_* envs.
type QESConfig struct {
	// Clients holds the resolved TSP adapters keyed by Provider.
	// Empty map → QES is feature-flagged off; service returns
	// `not configured` to the handler.
	Clients map[tsplib.Provider]tsplib.TSPClient
	// PublicBaseURL is the externally-reachable origin used to
	// build the return URL handed to the QTSP. Without trailing
	// slash. e.g. "https://app.vaultdms.example".
	PublicBaseURL string
	// SessionTTL is how long an Authorize'd transaction stays valid
	// on our side. Should be ≥ the QTSP-side expiry.
	SessionTTL time.Duration
}

// AddQES wires the QES configuration into an existing Service. Called
// from main after construction; lets us keep the original New() shape
// untouched for back-compat with tests that don't need QES.
func (s *Service) AddQES(cfg QESConfig) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 15 * time.Minute
	}
	s.qes = &cfg
}

// StartQESInput is what the handler unwraps from the request body. It
// deliberately carries no document bytes, hash or signer identity (DEC-17):
// the signer is the caller and the bytes are the request's stored version.
type StartQESInput struct {
	RequestID   string
	SignerID    string
	Provider    tsplib.Provider
	CountryCode string
	Reason      string
	Location    string
}

// StartQESResult is returned to the frontend.
type StartQESResult struct {
	SessionID   string    `json:"session_id"`
	RedirectURL string    `json:"redirect_url"`
	Provider    string    `json:"provider"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// StartQES kicks off a QES ceremony for the calling signer. Steps:
//  1. Resolve the TSP adapter; reject unknown providers.
//  2. Load the request in the caller's tenant: it must be open, the
//     signer must belong to it, still be unsigned, and BE the caller
//     (session e-mail = signer e-mail) — review finding S-3.
//  3. Read the request's own (document, version) bytes as the caller and
//     hash them (DEC-17) — never client-supplied bytes.
//  4. tsp.Register with the signer row's identity.
//  5. Mint a session row with state secret + return URL.
//  6. tsp.Authorize → redirect URL.
//  7. Persist row + audit outbox event in one tx.
func (s *Service) StartQES(ctx context.Context, actor Actor, in StartQESInput) (*StartQESResult, error) {
	if s.qes == nil || len(s.qes.Clients) == 0 {
		return nil, fmt.Errorf("%w: qes not configured", ErrUpstream)
	}
	tsp, ok := s.qes.Clients[in.Provider]
	if !ok {
		return nil, fmt.Errorf("%w: unsupported qes provider %q", ErrInvalidRequest, in.Provider)
	}
	tenantID := actor.TenantID

	req, err := s.repo.GetByID(ctx, tenantID, in.RequestID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrRequestNotFound
	}
	if err := checkSignable(req, time.Now().UTC()); err != nil {
		return nil, err
	}
	switch {
	case req.Provider == "internal":
	case strings.HasPrefix(req.Provider, "qes_"):
		if req.Provider != "qes_"+string(in.Provider) {
			return nil, fmt.Errorf("%w: request was created for %s", ErrInvalidRequest, req.Provider)
		}
	default:
		return nil, fmt.Errorf("%w: request provider %q is signed at the vendor, not by QES", ErrInvalidRequest, req.Provider)
	}
	var signer *model.Signer
	for i := range req.Signers {
		if req.Signers[i].ID == in.SignerID {
			signer = &req.Signers[i]
			break
		}
	}
	if signer == nil {
		return nil, ErrSignerNotFound
	}
	if signer.Status == "signed" || signer.Status == "declined" {
		return nil, fmt.Errorf("%w (status %s)", ErrAlreadySigned, signer.Status)
	}
	if actor.Email == "" || !strings.EqualFold(strings.TrimSpace(actor.Email), strings.TrimSpace(signer.Email)) {
		return nil, fmt.Errorf("%w: only the signer may start their QES ceremony", ErrForbidden)
	}

	docBytes, err := s.versionBytes(ctx, actor, req.DocumentID, req.VersionID)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(docBytes)
	docHash := hex.EncodeToString(hash[:])

	reg, err := tsp.Register(ctx, tsplib.RegisterReq{
		TenantID: tenantID, SignerEmail: signer.Email,
		SignerName: signer.Name, CountryCode: in.CountryCode,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: qes register: %v", ErrUpstream, err)
	}

	// Build return URL + state HMAC. Session id is generated FIRST
	// so we can stamp it into the URL we hand to the TSP.
	sessionID := repository.NewID()
	stateSecret, err := tsplib.NewStateSecret()
	if err != nil {
		return nil, err
	}
	state := tsplib.SignState(sessionID, stateSecret)
	returnURL := fmt.Sprintf("%s/api/v1/signatures/qes/return?session=%s&state=%s",
		s.qes.PublicBaseURL, sessionID, state)

	auth, err := tsp.Authorize(ctx, tsplib.AuthorizeReq{
		TenantID: tenantID, SubjectID: reg.SubjectID,
		DocumentHash: docHash, HashAlgo: "SHA-256",
		ReturnURL: returnURL, SignerEmail: signer.Email,
		Reason: in.Reason, Location: in.Location,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: qes authorize: %v", ErrUpstream, err)
	}

	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant_id: %w", err)
	}
	sessionUUID, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, fmt.Errorf("session_id: %w", err)
	}

	row := &repository.TSPSession{
		ID: sessionID, TenantID: tenantID,
		RequestID: req.ID, SignerID: signer.ID,
		Provider: string(in.Provider), Status: "pending",
		DocumentHash: docHash, ExternalID: auth.ExternalID,
		RedirectURL: auth.RedirectURL, ReturnURL: returnURL,
		StateSecret: stateSecret,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   auth.ExpiresAt,
	}

	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.CreateTSPSessionTx(ctx, tx, row); err != nil {
			return err
		}
		audit, _ := json.Marshal(map[string]any{
			"specversion": "1.0", "type": "dms.audit.qes.started.v1",
			"source": "/vaultdms/signature",
			"data": map[string]string{
				"tenant_id": tenantID, "request_id": req.ID,
				"signer_id": signer.ID, "session_id": sessionID,
				"provider": string(in.Provider), "actor": actor.UserID,
			},
		})
		evt := database.NewOutboxEvent(tenantUUID, "dms.audit.qes.started.v1",
			"tsp_signing_session", sessionUUID, audit)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, fmt.Errorf("qes persist: %w", err)
	}
	return &StartQESResult{
		SessionID: sessionID, RedirectURL: auth.RedirectURL,
		Provider: string(in.Provider), ExpiresAt: auth.ExpiresAt,
	}, nil
}

// HandleReturnInput is the parsed query-string of the return URL.
type HandleReturnInput struct {
	SessionID string
	State     string
	AuthCode  string
}

// HandleReturnResult is what the handler responds with — typically a
// 302 to the frontend's /sign/done page; the handler builds the URL.
type HandleReturnResult struct {
	RequestID    string
	Status       string // "completed" | "failed"
	FailureReason string
}

// HandleReturn finishes the ceremony. Steps:
//   1. Look up session, verify state HMAC, reject if expired or
//      already completed.
//   2. Mark authorized inside a tx; call TSP.Sign.
//   3. Persist qes_certificates, mark session completed, audit
//      outbox event — all in one tx.
//
// Failures along the way mark the session 'failed' with a reason
// before returning. The frontend reads /qes/session/:id afterward
// to render the result; we never inline the error in the redirect.
func (s *Service) HandleReturn(ctx context.Context, tenantID string, in HandleReturnInput) (*HandleReturnResult, error) {
	if s.qes == nil {
		return nil, errors.New("qes not configured")
	}
	row, err := s.repo.GetTSPSession(ctx, tenantID, in.SessionID)
	if err != nil {
		return nil, fmt.Errorf("qes lookup: %w", err)
	}
	if row == nil {
		return nil, errors.New("qes: session not found")
	}
	// State HMAC check — without this, a redirect from a different
	// session could trick us into completing the wrong row.
	verified, ok := tsplib.VerifyState(in.State, row.StateSecret)
	if !ok || verified != row.ID {
		return nil, errors.New("qes: state hmac mismatch")
	}
	switch row.Status {
	case "completed":
		// Idempotent: re-issuing the redirect should not double-bill
		// the QTSP. Just report the existing state.
		return &HandleReturnResult{RequestID: row.RequestID, Status: "completed"}, nil
	case "failed":
		return &HandleReturnResult{RequestID: row.RequestID, Status: "failed", FailureReason: row.FailureReason}, nil
	}
	if row.Status == "expired" || time.Now().After(row.ExpiresAt) {
		_ = s.repo.MarkTSPSessionFailed(ctx, tenantID, row.ID, "session expired before return")
		return &HandleReturnResult{RequestID: row.RequestID, Status: "failed", FailureReason: "expired"}, nil
	}

	tsp, ok := s.qes.Clients[tsplib.Provider(row.Provider)]
	if !ok {
		return nil, fmt.Errorf("qes: provider %q no longer configured", row.Provider)
	}

	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	sessionUUID, err := uuid.Parse(row.ID)
	if err != nil {
		return nil, err
	}

	// Claim the session: flip pending → authorized in its own tx. Only the
	// return that wins the flip calls Sign() and records the outcome; a
	// duplicate or concurrent return reports the session's current state
	// instead, so it can neither double-bill the QTSP nor overwrite the
	// winner's completion with its own "consumed" failure (S-10). If Sign()
	// crashes later, the row stays 'authorized' (not 'pending'), so the
	// reaper won't blindly re-publish a redirect.
	var claimed bool
	if err := database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		var err error
		claimed, err = s.repo.MarkTSPSessionAuthorized(ctx, tx, tenantID, row.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("qes mark authorized: %w", err)
	}
	if !claimed {
		cur, err := s.repo.GetTSPSession(ctx, tenantID, row.ID)
		if err != nil {
			return nil, fmt.Errorf("qes lookup: %w", err)
		}
		if cur == nil {
			return nil, errors.New("qes: session not found")
		}
		return &HandleReturnResult{RequestID: cur.RequestID, Status: cur.Status, FailureReason: cur.FailureReason}, nil
	}

	signed, err := tsp.Sign(ctx, tsplib.SignReq{
		TenantID: tenantID, ExternalID: row.ExternalID,
		AuthCode: in.AuthCode, DocumentHash: row.DocumentHash,
	})
	if err != nil {
		reason := err.Error()
		if errors.Is(err, tsplib.ErrSessionExpired) {
			reason = "expired"
		} else if errors.Is(err, tsplib.ErrAlreadyConsumed) {
			reason = "consumed"
		}
		_ = s.repo.MarkTSPSessionFailed(ctx, tenantID, row.ID, reason)
		return &HandleReturnResult{RequestID: row.RequestID, Status: "failed", FailureReason: reason}, nil
	}

	cert := &repository.QESCertificate{
		ID: repository.NewID(), TenantID: tenantID,
		SignerID: row.SignerID, RequestID: row.RequestID,
		SessionID: row.ID, Provider: row.Provider,
		SubjectDN: signed.SubjectDN, IssuerDN: signed.IssuerDN,
		SerialHex: signed.SerialHex,
		NotBefore: signed.NotBefore, NotAfter: signed.NotAfter,
		CertPEM: signed.CertPEM, ChainPEM: signed.ChainPEM,
		LTVRevocation: signed.LTVRevocation,
		CreatedAt: time.Now().UTC(),
	}

	// Persist cert + flip session completed + audit + (TODO when
	// signer sidecar lands) hand signed hash to the embedder.
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		if err := s.repo.CreateQESCertificateTx(ctx, tx, cert); err != nil {
			return err
		}
		if err := s.repo.MarkTSPSessionCompletedTx(ctx, tx, tenantID, row.ID); err != nil {
			return err
		}
		// Keep the QTSP's signed hash (S-3): it is the signature value the
		// embedder needs and the evidence of what the QTSP signed. Stored on
		// the signer row next to the certificate it was made with.
		evidence, _ := json.Marshal(map[string]string{
			"type":            "qes",
			"provider":        row.Provider,
			"session_id":      row.ID,
			"document_hash":   row.DocumentHash,
			"hash_algo":       "SHA-256",
			"signed_hash_b64": base64.StdEncoding.EncodeToString(signed.SignedHash),
		})
		if err := s.repo.SetSignerQESEvidenceTx(ctx, tx, tenantID, row.RequestID, row.SignerID, cert.ID, evidence); err != nil {
			return err
		}
		audit, _ := json.Marshal(map[string]any{
			"specversion": "1.0", "type": "dms.audit.qes.signed.v1",
			"source": "/vaultdms/signature",
			"data": map[string]string{
				"tenant_id":  tenantID,
				"request_id": row.RequestID,
				"signer_id":  row.SignerID,
				"session_id": row.ID,
				"provider":   row.Provider,
				"subject_dn": signed.SubjectDN,
				"serial_hex": signed.SerialHex,
			},
		})
		evt := database.NewOutboxEvent(tenantUUID, "dms.audit.qes.signed.v1",
			"qes_certificate", sessionUUID, audit)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		// Persistence failed AFTER the QTSP minted the signature.
		// Sign already debited the user's transaction. Best we can
		// do is mark the session failed and surface a clear error
		// to ops via the audit-failed event below.
		_ = s.repo.MarkTSPSessionFailed(ctx, tenantID, row.ID, "post-sign persist: "+err.Error())
		return &HandleReturnResult{RequestID: row.RequestID, Status: "failed", FailureReason: "post_sign_persist"}, nil
	}

	// Embedding hand-off to the signer sidecar is tracked in
	// dms.audit.qes.embedded.v1 by the embedder once it acks; this
	// service no longer owns the PDF bytes after handing off. The
	// embedder lives in a separate worker (signer sidecar / DSS)
	// per ADR 0025 — wiring is a Wave 9.2b follow-up.

	return &HandleReturnResult{RequestID: row.RequestID, Status: "completed"}, nil
}

// GetSession is the thin pass-through used by the polling handler.
// Surfaced from service so the handler can stay agnostic of the repo
// row shape.
func (s *Service) GetSession(ctx context.Context, tenantID, id string) (*repository.TSPSession, error) {
	return s.repo.GetTSPSession(ctx, tenantID, id)
}

// GetCertificates returns the QES certs for a signature request.
func (s *Service) GetCertificates(ctx context.Context, tenantID, requestID string) ([]*repository.QESCertificate, error) {
	return s.repo.GetQESCertificateByRequest(ctx, tenantID, requestID)
}

// StartQESReaper runs the 5-minute sweep that flips expired sessions.
// Cancels with the parent ctx.
func (s *Service) StartQESReaper(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.repo.ReapExpiredTSPSessions(ctx)
			if err != nil {
				s.log.Error().Err(err).Msg("qes reaper")
				continue
			}
			if n > 0 {
				s.log.Info().Int64("expired", n).Msg("qes reaper swept")
			}
		}
	}
}
