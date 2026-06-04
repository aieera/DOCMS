package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DocumentClient is the connector's outbound HTTP client to the document
// service. We use REST (gRPC-Gateway-mapped) rather than wiring a gRPC
// client because the document service exposes the same surface both
// ways and the REST path is one-file simpler — no proto-dep + creds
// shuffling. Auth headers mimic what the gateway would inject for a
// real user-bound request.
type DocumentClient struct {
	baseURL         string
	gatewaySecret   string
	internalKey     string // SEDOC_INTERNAL_API_KEY — internal-service auth
	hc              *http.Client
	log             func(format string, args ...any) // optional debug
}

// NewDocumentClient configures the client from env. baseURL defaults to
// the in-network DNS name; gatewaySecret comes from
// SEDOC_GATEWAY_SECRET (same env every service reads).
func NewDocumentClient() *DocumentClient {
	base := os.Getenv("SEDOC_DOCUMENT_HTTP_URL")
	if base == "" {
		base = "http://document:8080"
	}
	return &DocumentClient{
		baseURL:       base,
		gatewaySecret: os.Getenv("SEDOC_GATEWAY_SECRET"),
		internalKey:   os.Getenv("SEDOC_INTERNAL_API_KEY"),
		hc:            &http.Client{Timeout: 30 * time.Second},
	}
}

// CreateDocumentInput mirrors the fields the document service's
// CreateDocument endpoint accepts. We only set what the email path
// needs.
type CreateDocumentInput struct {
	WorkspaceID    string         `json:"workspace_id"`
	FolderID       string         `json:"folder_id"`
	Title          string         `json:"title"`
	Description    string         `json:"description,omitempty"`
	CustomMetadata map[string]any `json:"custom_metadata,omitempty"`
}

// CreateVersionInput is the body for POST /documents/{id}/versions.
type CreateVersionInput struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	BytesB64    string `json:"bytes_b64,omitempty"`
}

// CreateDocumentResp is the slice of the response we care about.
// The document service's REST mapping returns the document object
// directly, with the UUID in `id` (not `document_id`). gRPC-gateway
// flattens the response message, not the request shape.
type CreateDocumentResp struct {
	DocumentID string `json:"id"`
}

// MaterialiseFile is the generic single-file create path: caller hands
// us a workspace+folder, a filename, content-type, bytes, and arbitrary
// custom metadata; we create one document row (no version-blob upload
// yet — same §12.5c-deferred caveat as MaterialiseEmail). Used by the
// §12.4 intake worker for watched-folder ingestion.
func (c *DocumentClient) MaterialiseFile(
	ctx context.Context,
	tenantID, actorID string,
	workspaceID, folderID string,
	filename, contentType string,
	bytes []byte,
	customMetadata map[string]any,
) (string, []string, error) {
	if workspaceID == "" || folderID == "" {
		return "", nil, fmt.Errorf("target workspace + folder required")
	}
	if customMetadata == nil {
		customMetadata = map[string]any{}
	}
	customMetadata["intake.filename"] = filename
	customMetadata["intake.content_type"] = contentType
	customMetadata["intake.size_bytes"] = len(bytes)
	resp, err := c.createDocument(ctx, tenantID, actorID, CreateDocumentInput{
		WorkspaceID:    workspaceID,
		FolderID:       folderID,
		Title:          filename,
		CustomMetadata: customMetadata,
	})
	if err != nil {
		return "", nil, err
	}
	// NOTE on version-blob upload: see the comment in MaterialiseEmail.
	// Wave 12.5c lands the storage presigned-PUT + commit path so the
	// version row fires dms.version.uploaded.v1, which is what triggers
	// the OCR + classify pipelines. Today the document row exists but
	// has no version, so OCR doesn't run. The intake ADR acknowledges
	// this is the same gap.
	_ = bytes
	return resp.DocumentID, nil, nil
}

// MaterialiseEmail turns one envelope into a parent document (body) plus
// one child document per attachment. Caller passes tenant + actor IDs
// + the target folder. Returns the body's document_id and the list of
// attachment doc_ids so the email_messages row can be back-stamped.
//
// Failure mode is fail-fast: if the body doc fails to create, no
// attachments are tried; the email_messages row stays pending and the
// next tick retries. Partial success (body OK, attachment fails) is
// not yet handled — the email_messages.attachment_document_ids array
// gets whatever attachments succeeded; the worker doesn't re-try the
// missing one. That's a §12.5c follow-up; the failure rate in
// practice is low because both create paths run against the same
// service.
func (c *DocumentClient) MaterialiseEmail(
	ctx context.Context,
	tenantID, actorID string,
	cfg *Config,
	emailMessageID string,
	env *Envelope,
) (bodyDocID string, attachmentDocIDs []string, err error) {
	if cfg.TargetWorkspaceID == "" || cfg.TargetFolderID == "" {
		return "", nil, fmt.Errorf("config %s: target workspace + folder required", cfg.ID)
	}

	bodyMeta := map[string]any{
		"email.from":         env.From,
		"email.to":           env.To,
		"email.subject":      env.Subject,
		"email.received_at":  env.Date.Format(time.RFC3339),
		"email.thread_id":    env.ThreadID,
		"email.source":       string(cfg.Source),
		"email.message_id":   emailMessageID,
	}
	body, err := c.createDocument(ctx, tenantID, actorID, CreateDocumentInput{
		WorkspaceID:    cfg.TargetWorkspaceID,
		FolderID:       cfg.TargetFolderID,
		Title:          firstNonEmpty(env.Subject, "(no subject)"),
		Description:    "From " + env.From,
		CustomMetadata: bodyMeta,
	})
	if err != nil {
		return "", nil, fmt.Errorf("create body doc: %w", err)
	}
	// NOTE — version attachment is a follow-up step.
	//
	// CreateVersion requires a content_blob_id pointing at an
	// already-uploaded blob in the storage service (presigned PUT to
	// MinIO + commit). Threading that through here is its own commit
	// because it needs a StorageClient that can:
	//   1) call /storage/blobs/initiate to get a presigned URL,
	//   2) PUT the raw bytes,
	//   3) call /storage/blobs/{id}/complete to get the blob ID,
	//   4) pass that into CreateVersion.
	//
	// Without that, the body + each attachment land in the document
	// table but carry no version, so they don't appear in the document
	// list (which filters on having a version) and never enter the OCR
	// + classify pipelines that subscribe to dms.version.uploaded.v1.
	//
	// Marking the message materialised because the doc-row half of the
	// work succeeded; the version-upload sweep ships in wave-12.5b.
	for _, att := range env.Attachments {
		childMeta := map[string]any{
			"email.parent_message_id": emailMessageID,
			"email.parent_doc_id":     body.DocumentID,
			"email.from":              env.From,
			"email.received_at":       env.Date.Format(time.RFC3339),
			"email.filename":          att.Filename,
			"email.content_type":      att.ContentType,
			"email.size_bytes":        len(att.Bytes),
		}
		child, cerr := c.createDocument(ctx, tenantID, actorID, CreateDocumentInput{
			WorkspaceID:    cfg.TargetWorkspaceID,
			FolderID:       cfg.TargetFolderID,
			Title:          att.Filename,
			Description:    "Attachment of: " + env.Subject,
			CustomMetadata: childMeta,
		})
		if cerr != nil {
			c.debug("attach create failed: %v", cerr)
			continue
		}
		attachmentDocIDs = append(attachmentDocIDs, child.DocumentID)
	}
	return body.DocumentID, attachmentDocIDs, nil
}

func (c *DocumentClient) createDocument(ctx context.Context, tenantID, actorID string, in CreateDocumentInput) (*CreateDocumentResp, error) {
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/documents", bytes.NewReader(body))
	c.applyAuth(req, tenantID, actorID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("document create %d: %s", resp.StatusCode, string(b))
	}
	var out CreateDocumentResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *DocumentClient) createVersion(ctx context.Context, tenantID, actorID, docID string, in CreateVersionInput) error {
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/documents/"+docID+"/versions", bytes.NewReader(body))
	c.applyAuth(req, tenantID, actorID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("version create %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *DocumentClient) applyAuth(req *http.Request, tenantID, actorID string) {
	req.Header.Set("X-Gateway-Signature", c.gatewaySecret)
	// Internal-service auth: the worker has no user session, so it presents
	// the shared internal key and the identity it acts as. The document
	// service's SessionOrAPIKey trusts these headers only when the key
	// matches (pkg/middleware.SessionOrAPIKey); the gateway strips any
	// client-supplied X-Internal-Service-Key so this can't be spoofed.
	req.Header.Set("X-Internal-Service-Key", c.internalKey)
	req.Header.Set("X-Auth-Tenant-ID", tenantID)
	req.Header.Set("X-Tenant-ID", tenantID)
	if actorID != "" {
		req.Header.Set("X-User-ID", actorID)
		req.Header.Set("X-User-Role", "admin") // ingestion runs with admin perms
	}
}

func (c *DocumentClient) debug(format string, args ...any) {
	if c.log != nil {
		c.log(format, args...)
	}
}