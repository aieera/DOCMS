// ADR 0070 / 0071 follow-up — bytes-to-storage hand-off.
//
// When a QES ceremony or a DocuSign / Adobe Sign envelope completes,
// the vendor returns the signed PDF bytes (and optionally a
// Certificate of Completion). Without this hand-off those bytes are
// counted in an outbox event and discarded. Here we:
//
//   1. InitiateUpload at the storage service (presigned PUT URL).
//   2. PUT the bytes via plain HTTP.
//   3. CompleteUpload — triggers ClamAV + envelope encryption per
//      the existing storage flow.
//   4. Look up the content_blob_id by (tenant, sha256). storage's
//      CompleteUpload doesn't return it on the proto today; the
//      document service's storage proxy does the same lookup.
//   5. CreateVersion at the document service — that path mints the
//      version row + emits dms.version.uploaded.v1, which the
//      intelligence pipeline already subscribes to.
//
// The whole thing is wrapped behind an `IngestSignedClient` interface
// so tests can swap in an in-process double; the gRPC clients only
// exist in main.go.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IngestSignedClient abstracts the upload + version-create round
// trip. main.go wires the gRPC implementation; tests use the
// in-process double.
type IngestSignedClient interface {
	// PutSignedBlob uploads bytes through the storage service and
	// returns the resulting content_blob_id + sha256 for callers
	// that want to stamp provenance into a downstream event.
	PutSignedBlob(ctx context.Context, in PutSignedBlobInput) (*PutSignedBlobResult, error)
	// CreateVersionFromBlob mints a version on (document_id) bound
	// to the freshly-uploaded blob. Returns the new version_id.
	CreateVersionFromBlob(ctx context.Context, in CreateVersionFromBlobInput) (string, error)
}

// PutSignedBlobInput captures the bytes + their classification.
type PutSignedBlobInput struct {
	TenantID  string
	UserID    string
	Filename  string
	MimeType  string
	Bytes     []byte
	// RegionPin must match the document's region (ADR pin invariant).
	// Pulled from documents.region_pin by the caller.
	RegionPin string
}

// PutSignedBlobResult is returned by the storage round-trip.
type PutSignedBlobResult struct {
	ContentBlobID string
	SHA256        string
	SizeBytes     int64
}

// CreateVersionFromBlobInput is the document-service hand-off.
type CreateVersionFromBlobInput struct {
	TenantID      string
	UserID        string
	DocumentID    string
	ContentBlobID string
	ChangeSummary string
}

// ingestPipeline is what the QES + esign post-completion paths
// share. A nil client → ingest is a no-op (logged); the outbox
// event still emits with byte counts so observability is preserved.
type ingestPipeline struct {
	pool   *pgxpool.Pool
	client IngestSignedClient
}

// PutAndCreateVersion runs the full upload → create-version sequence.
// Returns (versionID, contentBlobID).
func (p *ingestPipeline) PutAndCreateVersion(ctx context.Context, in PutSignedBlobInput, documentID, userID, changeSummary string) (string, string, error) {
	if p.client == nil {
		return "", "", errors.New("ingest client not configured")
	}
	if len(in.Bytes) == 0 {
		return "", "", errors.New("signed bytes empty")
	}
	if in.MimeType == "" {
		in.MimeType = "application/pdf"
	}
	if in.RegionPin == "" {
		// Documents always have a region_pin; if caller didn't
		// resolve it, fall back to a sentinel storage understands.
		in.RegionPin = "us-east-1"
	}
	put, err := p.client.PutSignedBlob(ctx, in)
	if err != nil {
		return "", "", fmt.Errorf("upload signed blob: %w", err)
	}
	versionID, err := p.client.CreateVersionFromBlob(ctx, CreateVersionFromBlobInput{
		TenantID: in.TenantID, UserID: userID,
		DocumentID: documentID, ContentBlobID: put.ContentBlobID,
		ChangeSummary: changeSummary,
	})
	if err != nil {
		return "", "", fmt.Errorf("create version: %w", err)
	}
	return versionID, put.ContentBlobID, nil
}

// ResolveDocumentRegion looks up documents.region_pin from the same
// pool — both signature service and document service share the DB
// under tenant RLS. Returns "" + error when the document doesn't
// exist or RLS hides it.
func (p *ingestPipeline) ResolveDocumentRegion(ctx context.Context, tenantID, documentID string) (string, error) {
	if p.pool == nil {
		return "", errors.New("pool not configured")
	}
	var region string
	err := p.pool.QueryRow(ctx,
		`SELECT COALESCE(region_pin, '') FROM documents WHERE tenant_id = $1 AND id = $2`,
		tenantID, documentID).Scan(&region)
	return region, err
}

// hashBytes is exported here (lowercase wrapper around sha256) so
// the in-process test client can use the same canonicalization.
func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// httpPutWithRetry uploads to a presigned URL with one retry on
// transient transport error. Used by the gRPC-backed
// IngestSignedClient — kept here so the in-process double can
// reuse the same shape if it wants to test against a real httptest
// server.
func httpPutWithRetry(ctx context.Context, hc *http.Client, url string, body []byte, headers map[string]string) error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			if attempt == 0 {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return fmt.Errorf("put: %w", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		return fmt.Errorf("put: status %d", resp.StatusCode)
	}
	return errors.New("put: exhausted retries")
}
