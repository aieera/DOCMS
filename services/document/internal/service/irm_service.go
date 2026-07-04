package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// IRMService implements the protected-container export (§5/§8): seal a document
// version under a fresh payload DEK, wrap that DEK per recipient, issue licenses,
// gate opens through an online check callback (validate + log + revoke), and
// stream decrypted bytes to authorised recipients. Reuses pkg/crypto envelope +
// KeyManager; runs fully against LocalKeyManager + MinIO.
type IRMService struct {
	pool       *pgxpool.Pool
	repos      *repository.Repositories
	s3         *storage.S3Client
	kms        pkgcrypto.KeyManager
	doc        *DocumentService
	signingKey []byte
	log        zerolog.Logger
}

func NewIRMService(pool *pgxpool.Pool, repos *repository.Repositories, s3 *storage.S3Client, kms pkgcrypto.KeyManager, doc *DocumentService, log zerolog.Logger) *IRMService {
	return &IRMService{pool: pool, repos: repos, s3: s3, kms: kms, doc: doc, signingKey: irmSigningKey(), log: log}
}

func irmSigningKey() []byte {
	if k := os.Getenv("SEDOC_IRM_SIGNING_KEY"); k != "" {
		return []byte(k)
	}
	// Dev fallback mirrors the eDiscovery signer pattern; prod injects the env.
	return []byte("sedoc-dev-irm-signing-key-do-not-use-in-prod")
}

// ---- inputs / outputs ---------------------------------------------------

type IRMRecipientInput struct {
	Type string // user | email
	Ref  string
}

type IRMExportInput struct {
	DocumentID     uuid.UUID
	VersionID      uuid.UUID // Nil => current version
	Recipients     []IRMRecipientInput
	ExpiresAt      time.Time
	AllowedActions []string
}

type IRMIssuedLicense struct {
	LicenseID     uuid.UUID
	RecipientType string
	RecipientRef  string
	Token         string
	OpenURL       string
	PolicyHeader  model.IRMPolicyHeader
}

type IRMExportResult struct {
	ContainerID   uuid.UUID
	DocumentTitle string
	Licenses      []IRMIssuedLicense
}

type IRMCheckResult struct {
	AllowedActions []string
	ExpiresAt      time.Time
	Title          string
	Mime           string
	SenderEmail    string
}

const irmCallbackPath = "/api/v1/irm/licenses/check"

// irmMaxContentBytes bounds the in-memory read of a sealed container at open
// time (matches the storage single-PUT ceiling; overridable via env).
const irmMaxContentBytes int64 = 500 << 20 // 500 MiB

// ---- export -------------------------------------------------------------

// ExportProtected seals the document version and issues per-recipient licenses.
// The caller must be able to view the document (EnsureCanViewDocument).
func (s *IRMService) ExportProtected(ctx context.Context, in IRMExportInput) (*IRMExportResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.Recipients) == 0 {
		return nil, vdmserr.Validation("recipients", "at least one recipient required")
	}
	if in.ExpiresAt.Before(time.Now()) {
		return nil, vdmserr.Validation("expires_at", "must be in the future")
	}
	actions, err := normalizeActions(in.AllowedActions)
	if err != nil {
		return nil, err
	}
	for _, rcpt := range in.Recipients {
		if rcpt.Type != model.IRMRecipientUser && rcpt.Type != model.IRMRecipientEmail {
			return nil, vdmserr.Validation("recipient.type", "must be user|email")
		}
		if rcpt.Ref == "" {
			return nil, vdmserr.Validation("recipient.ref", "required")
		}
		// A user-bound recipient_ref MUST be a UUID — it is compared against the
		// opener's authenticated user id at check time, so a non-uuid ref would
		// bind to a recipient no session can ever satisfy.
		if rcpt.Type == model.IRMRecipientUser {
			if _, perr := uuid.Parse(rcpt.Ref); perr != nil {
				return nil, vdmserr.Validation("recipient.ref", "user recipient must be a valid user id (uuid)")
			}
		}
	}
	// Authorization: protect-sharing is a share, not a read — require the
	// "share" capability on the document, not merely view.
	if _, err := s.doc.requireDocPermission(ctx, tenantID, userID, in.DocumentID, "share"); err != nil {
		return nil, err
	}

	versionID := in.VersionID
	if versionID == uuid.Nil {
		if versionID, err = s.currentVersion(ctx, tenantID, in.DocumentID); err != nil {
			return nil, err
		}
	}

	// Read + seal the payload.
	plaintext, mime, title, srcBucket, err := s.readVersionPlaintext(ctx, tenantID, in.DocumentID, versionID)
	if err != nil {
		return nil, err
	}
	payloadDEK, err := pkgcrypto.GenerateDEK()
	if err != nil {
		return nil, err
	}
	defer zero(payloadDEK)
	ciphertext, nonce, err := pkgcrypto.EncryptData(plaintext, payloadDEK)
	zero(plaintext)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(ciphertext)
	containerID := uuid.New()
	sealedKey := fmt.Sprintf("%s/irm/%s.sealed", tenantID.String(), containerID.String())
	if err := s.s3.PutObject(ctx, srcBucket, sealedKey, bytes.NewReader(ciphertext), int64(len(ciphertext)), "application/octet-stream"); err != nil {
		return nil, fmt.Errorf("store sealed container: %w", err)
	}

	container := model.IRMContainer{
		TenantID: tenantID, ID: containerID, DocumentID: in.DocumentID, VersionID: versionID,
		SealedBucket: srcBucket, SealedKey: sealedKey, PayloadNonce: nonce, PayloadSHA256: sum[:],
		Mime: mime, Title: title, AllowedActions: actions, ExpiresAt: in.ExpiresAt, CreatedBy: userID,
	}

	issued := make([]IRMIssuedLicense, 0, len(in.Recipients))
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.repos.IRM.CreateContainer(ctx, tx, container); e != nil {
			return e
		}
		for _, rcpt := range in.Recipients {
			licenseID := uuid.New()
			keyRef := model.IRMLicenseKeyRef(tenantID, licenseID)
			wrapped, e := s.kms.EncryptDataKey(ctx, keyRef, payloadDEK)
			if e != nil {
				return e
			}
			token, e := randomToken()
			if e != nil {
				return e
			}
			th := sha256.Sum256([]byte(token))
			lic := model.IRMLicense{
				TenantID: tenantID, ID: licenseID, ContainerID: containerID,
				RecipientType: rcpt.Type, RecipientRef: rcpt.Ref, TokenHash: th[:],
				WrappedDEK: wrapped, KeyRef: keyRef, CreatedBy: userID,
			}
			if _, e := s.repos.IRM.CreateLicense(ctx, tx, lic); e != nil {
				return e
			}
			if e := s.repos.IRM.InsertEvent(ctx, tx, model.IRMLicenseEvent{
				TenantID: tenantID, ContainerID: containerID, LicenseID: &licenseID,
				EventType: "issued", RecipientRef: rcpt.Ref,
			}); e != nil {
				return e
			}
			if e := s.emitIRM(ctx, tx, tenantID, "dms.irm.license_issued.v1", containerID, map[string]any{
				"tenant_id": tenantID.String(), "container_id": containerID.String(),
				"license_id": licenseID.String(), "recipient_type": rcpt.Type, "recipient_ref": rcpt.Ref,
				"document_id": in.DocumentID.String(), "issued_by": userID.String(),
				"expires_at": in.ExpiresAt.UTC().Format(time.RFC3339),
			}); e != nil {
				return e
			}
			header := s.buildPolicyHeader(container, lic, token)
			issued = append(issued, IRMIssuedLicense{
				LicenseID: licenseID, RecipientType: rcpt.Type, RecipientRef: rcpt.Ref, Token: token,
				OpenURL:      fmt.Sprintf("/protected/%s?lt=%s", containerID.String(), token),
				PolicyHeader: header,
			})
		}
		return nil
	})
	if err != nil {
		// The sealed blob was written to S3 before the transaction; if the tx
		// failed the blob is orphaned (inert — no container/license references
		// it). Best-effort cleanup so it doesn't linger.
		if delErr := s.s3.DeleteObject(ctx, srcBucket, sealedKey); delErr != nil {
			s.log.Warn().Err(delErr).Str("key", sealedKey).Msg("irm: failed to clean up orphaned sealed blob after tx failure")
		}
		return nil, err
	}
	return &IRMExportResult{ContainerID: containerID, DocumentTitle: title, Licenses: issued}, nil
}

// ---- license-check callback ---------------------------------------------

// CheckLicense is the online callback: validate the license, log the open, and
// return open metadata. Cross-tenant-by-token; sessionUserID is the caller's
// authenticated user (uuid.Nil if anonymous) used to enforce internal binding.
// Returns a model.LicenseDenyReason ("" == allowed) plus the result on allow.
func (s *IRMService) CheckLicense(ctx context.Context, containerID uuid.UUID, token string, sessionUserID uuid.UUID, ipHash, userAgent string) (model.LicenseDenyReason, *IRMCheckResult, error) {
	lic, cont, err := s.lookup(ctx, containerID, token)
	if err != nil {
		return "", nil, err
	}
	if lic == nil {
		return model.LicenseDenyReason("not_found"), nil, nil
	}
	reason := model.ValidateLicenseOpen(*cont, *lic, time.Now(), sessionUserID)
	if reason != model.LicenseOK {
		// Log the denied attempt (best-effort, tenant-scoped).
		_ = s.withTenantTx(ctx, cont.TenantID, func(tx pgx.Tx) error {
			return s.repos.IRM.InsertEvent(ctx, tx, model.IRMLicenseEvent{
				TenantID: cont.TenantID, ContainerID: cont.ID, LicenseID: &lic.ID,
				EventType: "denied", RecipientRef: lic.RecipientRef, IPHash: ipHash, UserAgent: userAgent,
				Detail: string(reason),
			})
		})
		return reason, nil, nil
	}
	// Allowed: count the open, log it, emit dms.irm.opened.v1.
	senderEmail := ""
	err = s.withTenantTx(ctx, cont.TenantID, func(tx pgx.Tx) error {
		if e := s.repos.IRM.IncrementOpen(ctx, tx, cont.TenantID, lic.ID); e != nil {
			return e
		}
		if e := s.repos.IRM.InsertEvent(ctx, tx, model.IRMLicenseEvent{
			TenantID: cont.TenantID, ContainerID: cont.ID, LicenseID: &lic.ID,
			EventType: "opened", RecipientRef: lic.RecipientRef, IPHash: ipHash, UserAgent: userAgent,
		}); e != nil {
			return e
		}
		_ = tx.QueryRow(ctx, `SELECT COALESCE(email,'') FROM users WHERE tenant_id=$1 AND id=$2`, cont.TenantID, cont.CreatedBy).Scan(&senderEmail)
		return s.emitIRM(ctx, tx, cont.TenantID, "dms.irm.opened.v1", cont.ID, map[string]any{
			"tenant_id": cont.TenantID.String(), "container_id": cont.ID.String(), "license_id": lic.ID.String(),
			"recipient_ref": lic.RecipientRef, "document_id": cont.DocumentID.String(),
			"opened_at": time.Now().UTC().Format(time.RFC3339),
		})
	})
	if err != nil {
		return "", nil, err
	}
	return model.LicenseOK, &IRMCheckResult{
		AllowedActions: cont.AllowedActions, ExpiresAt: cont.ExpiresAt, Title: cont.Title,
		Mime: cont.Mime, SenderEmail: senderEmail,
	}, nil
}

// OpenContent re-validates the license and returns the decrypted payload bytes.
// The check callback logs the open; content re-validates (so a revoke between
// check and fetch still blocks) without double-logging.
func (s *IRMService) OpenContent(ctx context.Context, containerID uuid.UUID, token string, sessionUserID uuid.UUID) ([]byte, string, model.LicenseDenyReason, error) {
	lic, cont, err := s.lookup(ctx, containerID, token)
	if err != nil {
		return nil, "", "", err
	}
	if lic == nil {
		return nil, "", model.LicenseDenyReason("not_found"), nil
	}
	if reason := model.ValidateLicenseOpen(*cont, *lic, time.Now(), sessionUserID); reason != model.LicenseOK {
		return nil, "", reason, nil
	}
	payloadDEK, err := s.kms.DecryptDataKey(ctx, lic.KeyRef, lic.WrappedDEK)
	if err != nil {
		return nil, "", "", fmt.Errorf("unwrap payload key: %w", err)
	}
	defer zero(payloadDEK)

	obj, err := s.s3.GetObject(ctx, cont.SealedBucket, cont.SealedKey)
	if err != nil {
		return nil, "", "", fmt.Errorf("fetch sealed container: %w", err)
	}
	defer func() { _ = obj.Close() }()
	// Bound the in-memory read: reject a sealed blob larger than the cap rather
	// than OOM the process (LimitReader+1 so oversize is detectable).
	ciphertext, err := io.ReadAll(io.LimitReader(obj, irmMaxContentBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	defer zero(ciphertext)
	if int64(len(ciphertext)) > irmMaxContentBytes {
		return nil, "", "", fmt.Errorf("sealed container exceeds %d bytes", irmMaxContentBytes)
	}
	sum := sha256.Sum256(ciphertext)
	if subtle.ConstantTimeCompare(sum[:], cont.PayloadSHA256) != 1 {
		return nil, "", "", fmt.Errorf("sealed container integrity check failed")
	}
	plaintext, err := pkgcrypto.DecryptData(ciphertext, cont.PayloadNonce, payloadDEK)
	if err != nil {
		return nil, "", "", fmt.Errorf("decrypt payload: %w", err)
	}
	return plaintext, cont.Mime, model.LicenseOK, nil
}

// ---- revoke + dashboards ------------------------------------------------

func (s *IRMService) RevokeLicense(ctx context.Context, licenseID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		lic, e := s.repos.IRM.GetLicense(ctx, tx, tenantID, licenseID)
		if e != nil {
			return e
		}
		if lic == nil {
			return vdmserr.NotFound("license not found")
		}
		ok, e := s.repos.IRM.RevokeLicense(ctx, tx, tenantID, licenseID, userID)
		if e != nil {
			return e
		}
		if !ok {
			return vdmserr.NotFound("license not found or already revoked")
		}
		if e := s.repos.IRM.InsertEvent(ctx, tx, model.IRMLicenseEvent{
			TenantID: tenantID, ContainerID: lic.ContainerID, LicenseID: &licenseID,
			EventType: "revoked", RecipientRef: lic.RecipientRef, Detail: "revoked_by:" + userID.String(),
		}); e != nil {
			return e
		}
		return s.emitIRM(ctx, tx, tenantID, "dms.irm.revoked.v1", lic.ContainerID, map[string]any{
			"tenant_id": tenantID.String(), "container_id": lic.ContainerID.String(), "license_id": licenseID.String(),
			"recipient_ref": lic.RecipientRef, "revoked_by": userID.String(),
			"revoked_at": time.Now().UTC().Format(time.RFC3339),
		})
	})
}

// RevokeContainer is the container-level kill switch — revokes ALL of a
// container's licenses at once (ValidateLicenseOpen fails on container.RevokedAt).
func (s *IRMService) RevokeContainer(ctx context.Context, containerID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cont, e := s.repos.IRM.GetContainer(ctx, tx, tenantID, containerID)
		if e != nil {
			return e
		}
		if cont == nil {
			return vdmserr.NotFound("container not found")
		}
		ok, e := s.repos.IRM.RevokeContainer(ctx, tx, tenantID, containerID)
		if e != nil {
			return e
		}
		if !ok {
			return vdmserr.NotFound("container not found or already revoked")
		}
		if e := s.repos.IRM.InsertEvent(ctx, tx, model.IRMLicenseEvent{
			TenantID: tenantID, ContainerID: containerID, EventType: "revoked",
			Detail: "container_revoked_by:" + userID.String(),
		}); e != nil {
			return e
		}
		return s.emitIRM(ctx, tx, tenantID, "dms.irm.revoked.v1", containerID, map[string]any{
			"tenant_id": tenantID.String(), "container_id": containerID.String(),
			"scope": "container", "revoked_by": userID.String(),
			"revoked_at": time.Now().UTC().Format(time.RFC3339),
		})
	})
}

func (s *IRMService) ListContainers(ctx context.Context) ([]model.IRMContainerSummary, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.IRMContainerSummary
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.IRM.ListContainers(ctx, tx, tenantID)
		return err
	})
	return out, err
}

func (s *IRMService) ListLicenses(ctx context.Context, containerID uuid.UUID) ([]model.IRMLicense, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.IRMLicense
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.IRM.ListLicenses(ctx, tx, tenantID, containerID)
		return err
	})
	return out, err
}

// ---- helpers ------------------------------------------------------------

func (s *IRMService) withTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, fn)
}

// lookup resolves a license+container by token and confirms the token belongs to
// the requested container (prevents token/container confusion).
func (s *IRMService) lookup(ctx context.Context, containerID uuid.UUID, token string) (*model.IRMLicense, *model.IRMContainer, error) {
	if token == "" {
		return nil, nil, nil
	}
	th := sha256.Sum256([]byte(token))
	lic, cont, err := s.repos.IRM.LookupByToken(ctx, s.pool, th[:])
	if err != nil {
		return nil, nil, err
	}
	if lic == nil || cont.ID != containerID {
		return nil, nil, nil
	}
	return lic, cont, nil
}

func (s *IRMService) buildPolicyHeader(c model.IRMContainer, l model.IRMLicense, token string) model.IRMPolicyHeader {
	h := model.IRMPolicyHeader{
		Format: model.IRMFormat, ContainerID: c.ID.String(), TenantID: c.TenantID.String(),
		DocumentID: c.DocumentID.String(), VersionID: c.VersionID.String(), Title: c.Title, Mime: c.Mime,
		AllowedActions: c.AllowedActions, ExpiresAt: c.ExpiresAt.UTC(),
		RecipientType: l.RecipientType, RecipientRef: l.RecipientRef, LicenseToken: token,
		CallbackURL: irmCallbackPath, IssuedAt: time.Now().UTC(),
	}
	_ = h.Sign(s.signingKey)
	return h
}

func (s *IRMService) emitIRM(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, subject string, containerID uuid.UUID, payload map[string]any) error {
	evt, err := model.NewOutboxEvent(tenantID, subject, "irm_container", containerID, payload)
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

func (s *IRMService) currentVersion(ctx context.Context, tenantID, docID uuid.UUID) (uuid.UUID, error) {
	var v *uuid.UUID
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_version_id FROM documents WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, tenantID, docID).Scan(&v)
	})
	if err != nil || v == nil {
		return uuid.Nil, vdmserr.NotFound("document has no current version")
	}
	return *v, nil
}

// readVersionPlaintext loads + decrypts a version's blob (mirrors the
// decrypt-stream read path) and returns bytes + mime + title + source bucket.
func (s *IRMService) readVersionPlaintext(ctx context.Context, tenantID, docID, versionID uuid.UUID) (plaintext []byte, mime, title, bucket string, err error) {
	var key, kekID string
	var encryptedDEK, dekNonce []byte
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT b.storage_bucket, b.storage_key,
			       COALESCE(NULLIF(v.mime_type,''), NULLIF(b.mime_type,''), 'application/octet-stream'),
			       COALESCE(d.title,''), COALESCE(b.kek_id,''), b.encrypted_dek, b.dek_nonce
			FROM document_versions v
			JOIN content_blobs b ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
			JOIN documents d ON d.tenant_id = v.tenant_id AND d.id = v.document_id
			WHERE v.tenant_id=$1 AND v.document_id=$2 AND v.id=$3 AND b.shredded_at IS NULL
		`, tenantID, docID, versionID).Scan(&bucket, &key, &mime, &title, &kekID, &encryptedDEK, &dekNonce)
	})
	if err != nil {
		return nil, "", "", "", vdmserr.ErrNotFound
	}
	obj, err := s.s3.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, "", "", "", err
	}
	defer func() { _ = obj.Close() }()
	raw, err := io.ReadAll(io.LimitReader(obj, irmMaxContentBytes+1))
	if err != nil {
		return nil, "", "", "", err
	}
	if int64(len(raw)) > irmMaxContentBytes {
		return nil, "", "", "", fmt.Errorf("source blob exceeds %d bytes", irmMaxContentBytes)
	}
	if len(encryptedDEK) == 0 {
		// unencrypted source: raw IS the plaintext; caller zeroes it.
		return raw, mime, title, bucket, nil
	}
	defer zero(raw) // encrypted source ciphertext — clear after decrypt
	if s.kms == nil {
		return nil, "", "", "", fmt.Errorf("kms not configured")
	}
	plainDEK, err := s.kms.DecryptDataKey(ctx, kekID, encryptedDEK)
	if err != nil {
		return nil, "", "", "", err
	}
	defer zero(plainDEK)
	pt, err := pkgcrypto.DecryptData(raw, dekNonce, plainDEK)
	if err != nil {
		return nil, "", "", "", err
	}
	return pt, mime, title, bucket, nil
}

func normalizeActions(in []string) ([]string, error) {
	out := []string{model.IRMActionView}
	seen := map[string]bool{model.IRMActionView: true}
	for _, a := range in {
		if !model.ValidIRMAction(a) {
			return nil, vdmserr.Validation("allowed_actions", "invalid action: "+a)
		}
		if !seen[a] {
			out = append(out, a)
			seen[a] = true
		}
	}
	return out, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil { // crypto/rand (C3 invariant)
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
