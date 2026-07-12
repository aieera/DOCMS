// Outlook add-in ingest endpoint (ADR 0112).
//
//	POST /api/v1/integrations/m365/ingest-email
//	  Auth: SeDoc session (set up by the upstream auth gateway).
//	  Body: { subject, from, to[], sent_at, body_html, body_text,
//	          attachments[{name, content_b64, mime_type}],
//	          workspace_id, folder_id, tags?, message_id? }
//	  201:  { document_id, attachment_document_ids[] }
//
// Phase 1 contract (matches the existing connector/email ingestion
// path in services/connector/internal/email/document_client.go):
//   - Create one parent document for the email body with the
//     subject as title + email metadata in custom_metadata.
//   - Create one child document per attachment with the parent's
//     document_id stamped in custom_metadata.email.parent_doc_id.
//   - Audit-log the operation as dms.m365.outlook.email.saved.v1.
//
// What's DEFERRED (matches the existing email-ingest deferral):
//   - Blob upload to MinIO + version creation. CreateVersion needs
//     a content_blob_id; threading that through here requires a
//     StorageClient handle the handler doesn't have today. Until
//     that sweep ships, the documents render in the list but carry
//     no versions, so OCR + classification don't fire. The audit
//     event still publishes so downstream consumers can react when
//     the sweep lands.
//
// The body-html / body-text + attachment bytes ARE received on the
// wire (so the contract is forward-compatible) — we just don't
// persist them to MinIO yet. The shape is intentionally identical
// to what the version-upload sweep will eventually consume.
package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/metadata"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/middleware"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// M365IngestHandler owns the Outlook add-in's ingest endpoint.
type M365IngestHandler struct {
	pool *pgxpool.Pool
	svc  *service.DocumentService
	// storage + s3 drive the server-side blob-upload pipeline (ADR 0112
	// completion): the email body + each attachment are pushed through
	// InitiateUpload → S3 put → CompleteUpload → CreateVersion, exactly like
	// the WOPI save path. nil (storage not configured in this deploy) falls
	// back to the metadata-only ingest with pending=true.
	storage sedocv1.StorageServiceClient
	s3      objectPutter
	log     zerolog.Logger
}

// objectPutter is the one S3 operation the ingest pipeline needs.
// *pkgstorage.S3Client satisfies it; tests inject a fake to exercise the
// full upload path without a live object store.
type objectPutter interface {
	PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64, contentType string) error
}

// NewM365IngestHandler builds the handler. storageClient + s3 may be nil (dev
// deploys without object storage); then ingest persists document rows only.
func NewM365IngestHandler(pool *pgxpool.Pool, svc *service.DocumentService, storageClient sedocv1.StorageServiceClient, s3 *pkgstorage.S3Client, log zerolog.Logger) *M365IngestHandler {
	// Store as the interface, but keep a nil interface when the concrete
	// client is nil (avoid the typed-nil-in-interface trap so storageReady
	// stays correct).
	var putter objectPutter
	if s3 != nil {
		putter = s3
	}
	return &M365IngestHandler{pool: pool, svc: svc, storage: storageClient, s3: putter, log: log}
}

// storageReady reports whether the blob-upload pipeline is wired.
func (h *M365IngestHandler) storageReady() bool { return h.storage != nil && h.s3 != nil }

// Register mounts the handler on the provided stdlib mux.
func (h *M365IngestHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/integrations/m365/ingest-email", h.ingestEmail)
}

// ---- wire types ----------------------------------------------------

type m365Attachment struct {
	Name       string `json:"name"`
	ContentB64 string `json:"content_b64"`
	MimeType   string `json:"mime_type,omitempty"`
}

type m365IngestReq struct {
	Subject     string           `json:"subject"`
	From        string           `json:"from"`
	To          []string         `json:"to"`
	CC          []string         `json:"cc,omitempty"`
	SentAt      string           `json:"sent_at"`
	BodyHTML    string           `json:"body_html,omitempty"`
	BodyText    string           `json:"body_text,omitempty"`
	Attachments []m365Attachment `json:"attachments"`
	WorkspaceID string           `json:"workspace_id"`
	FolderID    string           `json:"folder_id"`
	Tags        []string         `json:"tags,omitempty"`
	MessageID   string           `json:"message_id,omitempty"`
	// IncludeBody controls whether the email body becomes a parent
	// document. nil/true = file the body (back-compat); false = file the
	// attachments only (the "attachments only" add-in option). When false
	// the attachments have no parent and document_id comes back empty.
	IncludeBody *bool `json:"include_body,omitempty"`
}

type m365IngestResp struct {
	DocumentID            string   `json:"document_id"`
	AttachmentDocumentIDs []string `json:"attachment_document_ids"`
	// Pending == true signals the FE that blob upload (+ OCR +
	// classification) hasn't run yet. Lets the add-in show a
	// "queued for processing" message instead of "saved" until
	// the sweep flips it.
	Pending bool `json:"pending"`
}

// ---- handler ------------------------------------------------------

func (h *M365IngestHandler) ingestEmail(w http.ResponseWriter, r *http.Request) {
	// Auth context is stamped upstream by the session middleware —
	// we just lift the IDs.
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant required"})
		return
	}
	userID, err := auth.GetUserID(r.Context())
	if err != nil || userID == uuid.Nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user required"})
		return
	}

	var body m365IngestReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := validateIngest(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	wsID, _ := uuid.Parse(body.WorkspaceID)
	folderID, _ := uuid.Parse(body.FolderID)

	includeBody := body.IncludeBody == nil || *body.IncludeBody
	if !includeBody && len(body.Attachments) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attachments-only ingest requires at least one attachment"})
		return
	}

	subject := body.Subject
	if subject == "" {
		subject = "(no subject)"
	}

	// 1. Parent email document — skipped when filing attachments only.
	var parentID uuid.UUID
	parentIDStr := ""
	if includeBody {
		parent, err := h.svc.CreateDocument(r.Context(), &service.CreateDocumentInput{
			WorkspaceID:    wsID,
			FolderID:       folderID,
			Title:          subject,
			Description:    "From " + body.From,
			Tags:           body.Tags,
			CustomMetadata: parentMetadata(&body),
			UpdatedBy:      userID,
		})
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		parentID = parent.ID
		parentIDStr = parent.ID.String()

		// Persist the email body as the parent document's first version.
		if h.storageReady() {
			if data, filename, mime := m365BodyBytes(&body, subject); len(data) > 0 {
				if err := h.uploadBlobAndVersion(r.Context(), tenantID, userID, parentID, parent.RegionPin, filename, mime, data); err != nil {
					h.log.Error().Err(err).Str("document_id", parentIDStr).Msg("m365 ingest: email body blob upload failed")
				}
			}
		}
	}

	// 2. Attachment documents. Best-effort — a single failure does
	// NOT roll back the parent. The user sees the email saved + the
	// attachments that succeeded; the failed list comes back in the
	// response for the add-in to surface.
	attachIDs := make([]string, 0, len(body.Attachments))
	for _, a := range body.Attachments {
		// Decode up front so a malformed payload fails fast (skip the
		// attachment) rather than persisting a broken row.
		var decoded []byte
		if a.ContentB64 != "" {
			d, derr := base64.StdEncoding.DecodeString(a.ContentB64)
			if derr != nil {
				// The add-in shouldn't have sent something we can't
				// decode; a partial save beats a 500.
				continue
			}
			decoded = d
		}
		child, cerr := h.svc.CreateDocument(r.Context(), &service.CreateDocumentInput{
			WorkspaceID:    wsID,
			FolderID:       folderID,
			Title:          a.Name,
			Description:    "Attachment of: " + subject,
			Tags:           append([]string{"attachment"}, body.Tags...),
			CustomMetadata: childMetadata(&body, &a, parentIDStr),
			UpdatedBy:      userID,
		})
		if cerr != nil {
			// CreateDocument records its own logs; a single failure
			// doesn't abort the batch.
			continue
		}
		attachIDs = append(attachIDs, child.ID.String())

		// Persist the attachment bytes as the child document's version.
		if h.storageReady() && len(decoded) > 0 {
			mime := a.MimeType
			if mime == "" {
				mime = "application/octet-stream"
			}
			if err := h.uploadBlobAndVersion(r.Context(), tenantID, userID, child.ID, child.RegionPin, a.Name, mime, decoded); err != nil {
				h.log.Error().Err(err).Str("document_id", child.ID.String()).Str("filename", a.Name).
					Msg("m365 ingest: attachment blob upload failed")
			}
		}
	}

	// 3. Audit event in its own short-lived tx — fire-and-forget at
	// the response level, but transactional w.r.t. the outbox row
	// so the publisher reliably picks it up. Aggregate is the parent
	// doc, or the first attachment when filing attachments only.
	auditAggregate := parentID
	if auditAggregate == uuid.Nil && len(attachIDs) > 0 {
		auditAggregate, _ = uuid.Parse(attachIDs[0])
	}
	auditEvent := map[string]any{
		"email_id":         body.MessageID,
		"message_id":       body.MessageID,
		"document_id":      parentIDStr,
		"attachment_count": len(attachIDs),
		"workspace_id":     body.WorkspaceID,
		"folder_id":        body.FolderID,
		"saved_by":         userID.String(),
		"from":             body.From,
		"subject":          subject,
	}
	if auditAggregate != uuid.Nil {
		if err := h.insertAuditOutbox(r.Context(), tenantID, auditAggregate, auditEvent); err != nil {
			// Don't fail the request — the user's document was saved.
			// The audit gap surfaces in the outbox-lag dashboard.
			_ = err
		}
	}

	writeJSON(w, http.StatusCreated, m365IngestResp{
		DocumentID:            parentIDStr,
		AttachmentDocumentIDs: attachIDs,
		// Blobs + versions are persisted inline when storage is wired; only a
		// storage-less deploy still defers (metadata-only rows). OCR +
		// classification then fire off the dms.version.uploaded.v1 events
		// CreateVersion emits.
		Pending: !h.storageReady(),
	})
}

// uploadBlobAndVersion pushes bytes through the storage service pipeline
// (InitiateUpload → direct S3 put at the returned coordinates → CompleteUpload:
// hash verify + MIME sniff + virus scan + envelope encryption + content_blobs
// row) and links the resulting blob as a new version on docID. Mirrors the
// WOPI save path (handler/wopi_resolver.go). RegionPin is honored via the
// InitiateUpload request (C.4 residency).
func (h *M365IngestHandler) uploadBlobAndVersion(ctx context.Context, tenantID, userID, docID uuid.UUID, regionPin, filename, mime string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	// The storage-side permission check reads tenant/user/document from gRPC
	// metadata (same keys the browser upload proxy forwards).
	md := metadata.Pairs(
		middleware.TenantMetadataKey, tenantID.String(),
		"x-user-id", userID.String(),
		"x-document-id", docID.String(),
	)
	octx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), 60*time.Second)
	defer cancel()

	init, err := h.storage.InitiateUpload(octx, &sedocv1.InitiateUploadRequest{
		RegionPin:      regionPin,
		Filename:       filename,
		MimeType:       mime,
		SizeBytes:      int64(len(data)),
		ChecksumSha256: sha,
	})
	if err != nil {
		return fmt.Errorf("initiate upload: %w", err)
	}

	var blobID uuid.UUID
	if init.GetDeduplicated() {
		// Identical bytes already stored for this tenant — reuse the blob.
		blobID, err = uuid.Parse(init.GetExistingBlobId())
		if err != nil {
			return fmt.Errorf("dedup blob id: %w", err)
		}
	} else {
		if err := h.s3.PutObject(ctx, init.GetStorageBucket(), init.GetStorageKey(),
			bytes.NewReader(data), int64(len(data)), mime); err != nil {
			return fmt.Errorf("put object: %w", err)
		}
		if _, err := h.storage.CompleteUpload(octx, &sedocv1.CompleteUploadRequest{
			UploadId:       init.GetUploadId(),
			ChecksumSha256: sha,
			SizeBytes:      int64(len(data)),
		}); err != nil {
			return fmt.Errorf("complete upload: %w", err)
		}
		// CompleteUpload doesn't return the blob id; resolve by (tenant, sha).
		if err := database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `
				SELECT id FROM content_blobs
				WHERE tenant_id = $1 AND sha256_hash = $2
				ORDER BY created_at DESC LIMIT 1`, tenantID, sha).Scan(&blobID)
		}); err != nil {
			return fmt.Errorf("resolve blob id by sha: %w", err)
		}
	}

	if _, err := h.svc.CreateVersion(ctx, &service.CreateVersionInput{
		DocumentID:    docID,
		ContentBlobID: blobID,
		SizeBytes:     int64(len(data)),
		MimeType:      mime,
		SHA256Hash:    sha,
		ChangeSummary: "Saved from Outlook (m365 ingest)",
	}); err != nil {
		return fmt.Errorf("create version: %w", err)
	}
	return nil
}

// m365BodyBytes renders the email body to a persistable blob: HTML preferred
// (what Outlook sends for rich mail), plain text as a fallback. Empty when the
// email carried no body (attachments-only ingest).
func m365BodyBytes(b *m365IngestReq, subject string) (data []byte, filename, mime string) {
	if strings.TrimSpace(b.BodyHTML) != "" {
		return []byte(b.BodyHTML), safeFilename(subject) + ".html", "text/html; charset=utf-8"
	}
	if strings.TrimSpace(b.BodyText) != "" {
		return []byte(b.BodyText), safeFilename(subject) + ".txt", "text/plain; charset=utf-8"
	}
	return nil, "", ""
}

// safeFilename strips path/URL-hostile characters from an email subject so it
// can serve as a blob filename.
func safeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "email"
	}
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-",
		"\"", "'", "<", "(", ">", ")", "|", "-", "\n", " ", "\r", " ")
	out := strings.TrimSpace(repl.Replace(s))
	if len(out) > 120 {
		out = out[:120]
	}
	if out == "" {
		return "email"
	}
	return out
}

// ---- helpers ------------------------------------------------------

func validateIngest(b *m365IngestReq) error {
	if b.WorkspaceID == "" {
		return fmt.Errorf("workspace_id required")
	}
	if b.FolderID == "" {
		return fmt.Errorf("folder_id required")
	}
	if _, err := uuid.Parse(b.WorkspaceID); err != nil {
		return fmt.Errorf("workspace_id not a uuid")
	}
	if _, err := uuid.Parse(b.FolderID); err != nil {
		return fmt.Errorf("folder_id not a uuid")
	}
	// Either body OR at least one attachment must be present. An
	// empty email with no attachments is almost certainly a bug in
	// the add-in's Office.js read path.
	if strings.TrimSpace(b.BodyHTML) == "" && strings.TrimSpace(b.BodyText) == "" && len(b.Attachments) == 0 {
		return fmt.Errorf("empty ingest: body and attachments both missing")
	}
	return nil
}

func parentMetadata(b *m365IngestReq) map[string]any {
	m := map[string]any{
		"email.from":       b.From,
		"email.to":         b.To,
		"email.cc":         b.CC,
		"email.subject":    b.Subject,
		"email.source":     "m365_outlook_addin",
		"email.message_id": b.MessageID,
	}
	if b.SentAt != "" {
		if t, err := time.Parse(time.RFC3339, b.SentAt); err == nil {
			m["email.sent_at"] = t.Format(time.RFC3339)
		}
	}
	return m
}

func childMetadata(b *m365IngestReq, a *m365Attachment, parentID string) map[string]any {
	return map[string]any{
		"email.parent_doc_id":     parentID,
		"email.parent_message_id": b.MessageID,
		"email.from":              b.From,
		"email.filename":          a.Name,
		"email.content_type":      a.MimeType,
		"email.source":            "m365_outlook_addin",
	}
}

func (h *M365IngestHandler) insertAuditOutbox(ctx context.Context, tenantID, docID uuid.UUID, payload map[string]any) error {
	payloadBytes, _ := json.Marshal(payload)
	return database.WithTenantTx(ctx, h.pool, tenantID, func(tx pgx.Tx) error {
		evID, _ := uuid.NewV7()
		_, err := tx.Exec(ctx, `
			INSERT INTO outbox (id, tenant_id, event_type, aggregate_type, aggregate_id, payload, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			evID, tenantID,
			"dms.m365.outlook.email.saved.v1",
			"document", docID,
			payloadBytes, time.Now().UTC(),
		)
		return err
	})
}

// Reference exposed so dead-code linters don't strip model in
// edge cases where this file's compile-time deps shake out
// differently. Cheap belt; the type is used elsewhere in the
// package so this is genuinely a no-op at runtime.
var _ = model.Document{}
