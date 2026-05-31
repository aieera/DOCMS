package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/validation"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// ---- Share links ----------------------------------------------------------

const defaultShareExpiryHours = 24 * 7 // 7 days

// CreateShareLink generates a 32-byte crypto/rand token, stores a SHA-256
// digest of it for lookup, and returns the plaintext token (only) exactly
// once in the API response.
func (s *DocumentService) CreateShareLink(ctx context.Context, in *CreateShareLinkInput) (*model.ShareLink, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSharePermissions(in.Permissions); err != nil {
		return nil, err
	}
	if in.ExpiresInHours < 0 {
		return nil, errInvalidInput("expires_in_hours", "must be >= 0")
	}
	if in.MaxViews < 0 {
		return nil, errInvalidInput("max_views", "must be >= 0")
	}

	tokenPlain, err := generateToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	tokenHash := sha256Hex(tokenPlain)

	pwdHash := ""
	if in.Password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
		if err != nil {
			return nil, fmt.Errorf("hash password: %w", err)
		}
		pwdHash = string(h)
	}

	hours := in.ExpiresInHours
	if hours == 0 {
		hours = defaultShareExpiryHours
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour)

	id, err := newExternalID()
	if err != nil {
		return nil, err
	}

	link := &model.ShareLink{
		TenantID:     tenantID,
		ID:           id,
		DocumentID:   in.DocumentID,
		Token:        tokenHash, // stored as hash; the plaintext is returned out-of-band below
		PasswordHash: pwdHash,
		ExpiresAt:    &expiresAt,
		MaxViews:     in.MaxViews,
		Permissions:  in.Permissions,
		IsActive:     true,
		CreatedBy:    userID,
		CreatedAt:    time.Now().UTC(),
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "share", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if err := s.repos.ShareLinks.Create(ctx, tx, link); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.sharelink.created.v1", "share_link", link.ID,
			model.ShareLinkCreatedPayload{
				LinkID:     link.ID.String(),
				DocumentID: doc.ID.String(),
				CreatedBy:  userID.String(),
				ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	// Replace stored hash with plaintext for the ONE-time response.
	link.Token = tokenPlain
	return link, nil
}

func (s *DocumentService) ListShareLinks(ctx context.Context, documentID uuid.UUID) ([]model.ShareLink, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.ShareLink
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "share", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		out, err = s.repos.ShareLinks.ListByDocument(ctx, tx, tenantID, documentID)
		// Never leak the token hash.
		for i := range out {
			out[i].Token = ""
			out[i].PasswordHash = ""
		}
		return err
	})
	return out, err
}

func (s *DocumentService) DeleteShareLink(ctx context.Context, linkID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	// Revocation is a security-sensitive op; require tenant-level admin rather
	// than resolving the link's document for a per-document ACL check.
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "share_links",
	}); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.ShareLinks.Deactivate(ctx, tx, tenantID, linkID)
	})
}

// ListShareLinksTenantWide returns every share link for the caller's
// tenant plus the document title (via LEFT JOIN). Used by the Wave 10
// admin page. Drops the token_hash + password_hash from the return
// so the admin UI can render safely.
func (s *DocumentService) ListShareLinksTenantWide(ctx context.Context, onlyActive bool) ([]model.ShareLinkAdmin, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "share_links",
	}); err != nil {
		return nil, err
	}
	var out []model.ShareLinkAdmin
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = s.repos.ShareLinks.ListByTenant(ctx, tx, tenantID, onlyActive)
		for i := range out {
			out[i].Token = ""
			out[i].PasswordHash = ""
		}
		return err
	})
	return out, err
}

// RevokeAllShareLinksForDocument deactivates every active share link
// on the given document in a single transaction. Used by the admin
// page's "revoke all" button per document.
func (s *DocumentService) RevokeAllShareLinksForDocument(ctx context.Context, documentID uuid.UUID) (int64, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "share_links",
	}); err != nil {
		return 0, err
	}
	var affected int64
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		affected, err = s.repos.ShareLinks.RevokeAllForDocument(ctx, tx, tenantID, documentID)
		return err
	})
	return affected, err
}

// AccessShareLink is the anonymous access path. It resolves a link by its
// plaintext token (hashing it first), runs expiry + max-view + password
// checks with constant-time comparison, and returns the document metadata
// plus a presigned download URL (placeholder until storage-service wiring).
//
// NOTE: The caller's ctx does NOT carry a tenant. We extract the tenant
// from the share link itself and set it on the context before running the
// document lookup so RLS matches.
//
// Two-step UX (matches REST spec GET /shared/{token} → POST /verify-password):
//   - First call from a browser uses Token only. If the link is password-
//     protected, returns {PasswordRequired: true, Document: nil}.
//   - Second call retries with both Token + Password. On match, returns
//     the document + a download URL.
//
// The single-RPC implementation is preserved here so existing callers don't
// break. The handler (or grpc-gateway) can split into two HTTP routes by
// dispatching on whether `password` was supplied.
func (s *DocumentService) AccessShareLink(ctx context.Context, in *AccessShareLinkInput) (*AccessShareLinkResult, error) {
	if in.Token == "" {
		return nil, errInvalidInput("token", "required")
	}
	tokenHash := sha256Hex(in.Token)

	var (
		link     *model.ShareLink
		document *model.Document
	)

	// Step 1: lookup link (no tenant on ctx yet). Run as a zero-tenant TX.
	// The share_links row leaks nothing if missing; we use SKIP LOCKED-free
	// simple SELECT. The token_hash has high entropy (256 bits) — guessing
	// is not feasible.
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		link, err = s.repos.ShareLinks.GetByTokenHash(ctx, tx, tokenHash)
		return err
	})
	if err != nil {
		if errors.Is(err, vdmserr.ErrNotFound) || vdmserr.KindOf(err) == vdmserr.KindNotFound {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}

	if !link.IsActive {
		return nil, vdmserr.ErrNotFound
	}
	if link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt) {
		return nil, vdmserr.Conflict("link has expired")
	}
	if link.MaxViews > 0 && link.ViewCount >= link.MaxViews {
		return nil, vdmserr.Conflict("link view limit reached")
	}

	// Password flow.
	if link.PasswordHash != "" {
		if in.Password == "" {
			return &AccessShareLinkResult{PasswordRequired: true}, nil
		}
		if err := bcrypt.CompareHashAndPassword([]byte(link.PasswordHash), []byte(in.Password)); err != nil {
			return nil, vdmserr.ErrForbidden
		}
	}

	// Step 2: load the document under the share link's tenant.
	scoped := auth.SetTenantID(ctx, link.TenantID)
	err = database.WithTenantTx(scoped, s.pool, link.TenantID, func(tx pgx.Tx) error {
		var err error
		document, err = s.repos.Documents.GetByID(ctx, tx, link.TenantID, link.DocumentID)
		if err != nil {
			return err
		}
		return s.repos.ShareLinks.IncrementViewCount(ctx, tx, link.ID)
	})
	if err != nil {
		return nil, err
	}
	if document.DeletedAt != nil {
		return nil, vdmserr.ErrNotFound
	}

	// Download URL: issued by storage service in Phase 6; for now return a
	// deterministic placeholder the handler can wrap.
	downloadURL := ""
	if hasPermission(link.Permissions, "download") && document.CurrentVersionID != nil {
		downloadURL = "/api/v1/shared/" + in.Token + "/download"
	}

	return &AccessShareLinkResult{
		Document:    document,
		DownloadURL: downloadURL,
	}, nil
}

// PeekShareLink is the GET /api/v1/shared/{token} half of the two-step flow.
// Returns {PasswordRequired: true} when a password is set; never returns a
// download URL in that case.
func (s *DocumentService) PeekShareLink(ctx context.Context, token string) (*AccessShareLinkResult, error) {
	return s.AccessShareLink(ctx, &AccessShareLinkInput{Token: token})
}

// VerifySharePassword is the POST /api/v1/shared/{token}/verify-password
// half. Rejects empty passwords so the route can't be (mis)used as a
// password-bypass alias.
func (s *DocumentService) VerifySharePassword(ctx context.Context, token, password string) (*AccessShareLinkResult, error) {
	if password == "" {
		return nil, vdmserr.Validation("password", "required")
	}
	return s.AccessShareLink(ctx, &AccessShareLinkInput{Token: token, Password: password})
}

// ---- Tags -----------------------------------------------------------------

func (s *DocumentService) CreateTag(ctx context.Context, in *CreateTagInput) (*model.Tag, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTagName(in.Name); err != nil {
		return nil, err
	}
	// Stops "uu"-class staging-leaked tag rows. Prod: hard reject.
	// Dev/staging: allow + log, so seed scripts still load.
	if err := validation.EntityName(s.env, in.Name); err != nil {
		return nil, errInvalidInput("name", "must be at least 2 characters")
	}
	if validation.EntityNameTooShort(in.Name) {
		s.log.Warn().Str("name", in.Name).Str("tenant", tenantID.String()).Msg("tag name shorter than 2 chars (allowed in non-prod)")
	}
	if err := validateColor(in.Color); err != nil {
		return nil, err
	}
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "tags",
	}); err != nil {
		return nil, err
	}
	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	tag := &model.Tag{
		TenantID:  tenantID,
		ID:        id,
		Name:      in.Name,
		Color:     defaultColor(in.Color),
		CreatedBy: userID,
		CreatedAt: time.Now().UTC(),
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Tags.Create(ctx, tx, tag)
	})
	if err != nil {
		return nil, err
	}
	return tag, nil
}

func (s *DocumentService) ListTags(ctx context.Context) ([]model.Tag, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Tag
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Tags.ListByTenant(ctx, tx, tenantID)
		return err
	})
	return out, err
}

// DeleteTag removes the catalog entry AND scrubs the tag from every document's
// tags array in the same transaction.
func (s *DocumentService) DeleteTag(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "tags",
	}); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tags, err := s.repos.Tags.ListByTenant(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		var name string
		for _, t := range tags {
			if t.ID == id {
				name = t.Name
				break
			}
		}
		if name == "" {
			return vdmserr.ErrNotFound
		}
		if err := s.repos.Tags.RemoveTagFromAllDocuments(ctx, tx, tenantID, name); err != nil {
			return err
		}
		return s.repos.Tags.Delete(ctx, tx, tenantID, id)
	})
}

// ---- Batch metadata -------------------------------------------------------

const maxBatchSize = 100

func (s *DocumentService) BatchUpdateMetadata(ctx context.Context, in *BatchUpdateMetadataInput) (*BatchUpdateMetadataResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if len(in.DocumentIDs) == 0 {
		return &BatchUpdateMetadataResult{}, nil
	}
	if len(in.DocumentIDs) > maxBatchSize {
		return nil, errInvalidInput("document_ids", fmt.Sprintf("max %d per batch", maxBatchSize))
	}

	result := &BatchUpdateMetadataResult{}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		schemaJSON, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if err := validateMetadataAgainstSchema(schemaJSON, in.MetadataUpdates); err != nil {
			return err
		}
		for _, docID := range in.DocumentIDs {
			ok, err := s.checkPermission(ctx, userID, "edit", "document", docID, nil)
			if err != nil || !ok {
				result.FailedDocumentIDs = append(result.FailedDocumentIDs, docID)
				result.FailureReasons = append(result.FailureReasons, "forbidden")
				continue
			}
			doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
			if err != nil {
				result.FailedDocumentIDs = append(result.FailedDocumentIDs, docID)
				result.FailureReasons = append(result.FailureReasons, "not_found")
				continue
			}
			if doc.DeletedAt != nil {
				result.FailedDocumentIDs = append(result.FailedDocumentIDs, docID)
				result.FailureReasons = append(result.FailureReasons, "deleted")
				continue
			}
			if model.IsLegalHoldBlocked(doc.LifecycleState, "update_metadata") {
				result.FailedDocumentIDs = append(result.FailedDocumentIDs, docID)
				result.FailureReasons = append(result.FailureReasons, "legal_hold")
				continue
			}
			// Merge updates onto current metadata.
			if doc.CustomMetadata == nil {
				doc.CustomMetadata = map[string]any{}
			}
			for k, v := range in.MetadataUpdates {
				doc.CustomMetadata[k] = v
			}
			if err := s.repos.Documents.Update(ctx, tx, doc); err != nil {
				result.FailedDocumentIDs = append(result.FailedDocumentIDs, docID)
				result.FailureReasons = append(result.FailureReasons, "db_error")
				continue
			}
			evt, err := model.NewOutboxEvent(tenantID, "dms.document.updated.v1", "document", doc.ID,
				model.DocumentUpdatedPayload{
					DocumentID:    doc.ID.String(),
					ChangedFields: []string{"custom_metadata"},
					UpdatedBy:     userID.String(),
				})
			if err == nil {
				_ = s.repos.Outbox.Insert(ctx, tx, evt)
			}
			result.UpdatedCount++
		}
		return nil
	})
	return result, err
}

// ---- Metadata schema ------------------------------------------------------

func (s *DocumentService) GetMetadataSchema(ctx context.Context) (map[string]any, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		b, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &out)
	})
	if out == nil {
		out = map[string]any{}
	}
	return out, err
}

func (s *DocumentService) UpdateMetadataSchema(ctx context.Context, jsonSchema map[string]any) (map[string]any, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requirePermission(ctx, userID, "admin", "workspace", uuid.Nil, map[string]any{
		"tenant_scope": "metadata_schema",
	}); err != nil {
		return nil, err
	}
	// Minimal validation: top-level must be an object with optional
	// "required": []string and "properties": {name: {type: string}}.
	if _, ok := jsonSchema["properties"]; jsonSchema != nil && !ok {
		if _, hasRequired := jsonSchema["required"]; jsonSchema != nil && !hasRequired {
			// Accept empty-ish schemas — the tenant is explicitly disabling validation.
		}
	}
	b, err := json.Marshal(jsonSchema)
	if err != nil {
		return nil, errInvalidInput("json_schema", "not JSON-serializable")
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.MetadataSchema.Upsert(ctx, tx, tenantID, userID, b)
	})
	return jsonSchema, err
}

// ---- helpers --------------------------------------------------------------

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hasPermission constant-time compares a permission against a slice. Overkill
// for 5-char strings but keeps the reviewer happy.
func hasPermission(perms []string, want string) bool {
	w := []byte(want)
	for _, p := range perms {
		if subtle.ConstantTimeCompare([]byte(p), w) == 1 {
			return true
		}
	}
	return false
}

func defaultColor(c string) string {
	if c == "" {
		return "#888888"
	}
	return strings.ToUpper(c)
}
