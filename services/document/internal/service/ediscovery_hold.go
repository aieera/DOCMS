// Hold-scoped e-discovery export (§9.5 / G9 extension). Given a legal hold and
// an optional title/metadata refinement, resolve every in-scope document and
// build an EDRM-style export bundle — manifest + Concordance DAT load file +
// per-doc metadata + per-doc AUDIT TRAIL — asynchronously, staged to object
// storage for download. The export itself is audited (dms.export.completed.v1).
package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// ---- scope ---------------------------------------------------------------

// HoldScope is the preview of what a hold-scoped export will contain.
type HoldScope struct {
	HoldID    string      `json:"hold_id"`
	Query     string      `json:"query,omitempty"`
	DocCount  int         `json:"doc_count"`
	SizeBytes int64       `json:"size_bytes"`
	DocIDs    []uuid.UUID `json:"-"`
}

// ResolveHoldScope returns the in-scope documents for a hold, narrowed by an
// optional title/metadata filter. The hold's binding is the authoritative
// scope; the query only narrows it.
func (s *DocumentService) ResolveHoldScope(ctx context.Context, holdID uuid.UUID, query string) (*HoldScope, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	return s.resolveScope(ctx, tenantID, holdID, query)
}

func (s *DocumentService) resolveScope(ctx context.Context, tenantID, holdID uuid.UUID, query string) (*HoldScope, error) {
	scope := &HoldScope{HoldID: holdID.String(), Query: query}
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id, COALESCE(v.size_bytes, 0)
			  FROM legal_hold_documents lhd
			  JOIN documents d
			    ON d.tenant_id = lhd.tenant_id AND d.id = lhd.document_id AND d.deleted_at IS NULL
			  LEFT JOIN document_versions v
			    ON v.tenant_id = d.tenant_id AND v.id = d.current_version_id
			 WHERE lhd.tenant_id = $1 AND lhd.hold_id = $2
			   AND ($3 = '' OR d.title ILIKE '%' || $3 || '%')
			 ORDER BY d.created_at ASC`,
			tenantID, holdID, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var sz int64
			if err := rows.Scan(&id, &sz); err != nil {
				return err
			}
			scope.DocIDs = append(scope.DocIDs, id)
			scope.SizeBytes += sz
		}
		scope.DocCount = len(scope.DocIDs)
		return rows.Err()
	})
	return scope, err
}

// ---- async export job ----------------------------------------------------

// ExportJob is one async hold-scoped export.
type ExportJob struct {
	ID            string     `json:"id"`
	HoldID        string     `json:"hold_id,omitempty"`
	Query         string     `json:"query,omitempty"`
	Format        string     `json:"format"`
	Status        string     `json:"status"`
	DocCount      int        `json:"doc_count"`
	SizeBytes     int64      `json:"size_bytes"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DownloadReady bool       `json:"download_ready"`
}

// CreateExportJob inserts a pending job and kicks off the background build.
func (s *DocumentService) CreateExportJob(ctx context.Context, holdID uuid.UUID, query, format string) (*ExportJob, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if format == "" {
		format = "edrm"
	}
	job := &ExportJob{HoldID: holdID.String(), Query: query, Format: format, Status: "pending"}
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO export_jobs (tenant_id, hold_id, query, format, status, requested_by)
			VALUES ($1, $2, NULLIF($3,''), $4, 'pending', $5)
			RETURNING id::text, created_at, updated_at`,
			tenantID, holdID, query, format, userID,
		).Scan(&job.ID, &job.CreatedAt, &job.UpdatedAt)
	}); err != nil {
		return nil, vdmserr.FromPgError(err)
	}
	jobID := uuid.MustParse(job.ID)
	// Detach from the request context (preserves the auth values, drops the
	// request cancellation) so the build outlives the HTTP response.
	bg := context.WithoutCancel(ctx)
	go s.runExportJob(bg, tenantID, userID, jobID, holdID, query, format)
	return job, nil
}

func (s *DocumentService) runExportJob(ctx context.Context, tenantID, userID, jobID, holdID uuid.UUID, query, format string) {
	s.setJobStatus(ctx, tenantID, jobID, "running", "")

	scope, err := s.resolveScope(ctx, tenantID, holdID, query)
	if err != nil {
		s.setJobStatus(ctx, tenantID, jobID, "failed", "scope: "+err.Error())
		return
	}
	if s.s3 == nil {
		s.setJobStatus(ctx, tenantID, jobID, "failed", "object storage not configured")
		return
	}

	var buf bytes.Buffer
	if err := s.buildEDRMZip(ctx, tenantID, userID, &buf, holdID, query, scope.DocIDs); err != nil {
		s.setJobStatus(ctx, tenantID, jobID, "failed", "build: "+err.Error())
		return
	}

	bucket := "sedoc-exports"
	key := tenantID.String() + "/export-" + jobID.String() + ".zip"
	_ = s.s3.CreateBucket(ctx, bucket, "us-east-1")
	if err := s.s3.PutObject(ctx, bucket, key, bytes.NewReader(buf.Bytes()), int64(buf.Len()), "application/zip"); err != nil {
		s.setJobStatus(ctx, tenantID, jobID, "failed", "stage: "+err.Error())
		return
	}

	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `
			UPDATE export_jobs SET status='completed', doc_count=$3, size_bytes=$4,
			    storage_bucket=$5, storage_key=$6, completed_at=now(), updated_at=now()
			 WHERE tenant_id=$1 AND id=$2`,
			tenantID, jobID, scope.DocCount, int64(buf.Len()), bucket, key)
		if e != nil {
			return e
		}
		// dms.export.completed.v1 — the export itself is audited (the audit
		// service consumes dms.export.>).
		evt, e := model.NewOutboxEvent(tenantID, "dms.export.completed.v1", "export_job", jobID, map[string]any{
			"export_job_id": jobID.String(),
			"hold_id":       holdID.String(),
			"query":         query,
			"format":        format,
			"doc_count":     scope.DocCount,
			"size_bytes":    buf.Len(),
			"exported_by":   userID.String(),
		})
		if e != nil {
			return e
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	}); err != nil {
		s.setJobStatus(ctx, tenantID, jobID, "failed", "finalize: "+err.Error())
	}
}

func (s *DocumentService) setJobStatus(ctx context.Context, tenantID, jobID uuid.UUID, status, errMsg string) {
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`UPDATE export_jobs SET status=$3, error=NULLIF($4,''), updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenantID, jobID, status, errMsg)
		return e
	}); err != nil {
		s.log.Error().Err(err).Str("job", jobID.String()).Msg("export job status update failed")
	}
}

// GetExportJob returns one job's status.
func (s *DocumentService) GetExportJob(ctx context.Context, jobID uuid.UUID) (*ExportJob, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	job := &ExportJob{}
	var holdID *string
	var key *string
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text, hold_id::text, COALESCE(query,''), format, status, doc_count,
			       size_bytes, COALESCE(error,''), created_at, updated_at, completed_at, storage_key
			  FROM export_jobs WHERE tenant_id=$1 AND id=$2`,
			tenantID, jobID,
		).Scan(&job.ID, &holdID, &job.Query, &job.Format, &job.Status, &job.DocCount,
			&job.SizeBytes, &job.Error, &job.CreatedAt, &job.UpdatedAt, &job.CompletedAt, &key)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, vdmserr.NotFound("export job not found")
		}
		return nil, vdmserr.FromPgError(err)
	}
	if holdID != nil {
		job.HoldID = *holdID
	}
	job.DownloadReady = job.Status == "completed" && key != nil && *key != ""
	return job, nil
}

// ListExportJobs returns recent jobs for the tenant.
func (s *DocumentService) ListExportJobs(ctx context.Context) ([]*ExportJob, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := []*ExportJob{}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, hold_id::text, COALESCE(query,''), format, status, doc_count,
			       size_bytes, COALESCE(error,''), created_at, updated_at, completed_at,
			       (status='completed' AND storage_key IS NOT NULL)
			  FROM export_jobs WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT 50`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j := &ExportJob{}
			var holdID *string
			if err := rows.Scan(&j.ID, &holdID, &j.Query, &j.Format, &j.Status, &j.DocCount,
				&j.SizeBytes, &j.Error, &j.CreatedAt, &j.UpdatedAt, &j.CompletedAt, &j.DownloadReady); err != nil {
				return err
			}
			if holdID != nil {
				j.HoldID = *holdID
			}
			out = append(out, j)
		}
		return rows.Err()
	})
	return out, err
}

// DownloadExportJob streams a completed export's bytes from object storage.
func (s *DocumentService) DownloadExportJob(ctx context.Context, jobID uuid.UUID, w io.Writer) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	var bucket, key, status string
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status, COALESCE(storage_bucket,''), COALESCE(storage_key,'') FROM export_jobs WHERE tenant_id=$1 AND id=$2`,
			tenantID, jobID).Scan(&status, &bucket, &key)
	}); err != nil {
		if err == pgx.ErrNoRows {
			return vdmserr.NotFound("export job not found")
		}
		return vdmserr.FromPgError(err)
	}
	if status != "completed" || key == "" {
		return vdmserr.Conflict("export not ready")
	}
	if s.s3 == nil {
		return vdmserr.Internal("object storage not configured")
	}
	rc, err := s.s3.GetObject(ctx, bucket, key)
	if err != nil {
		return fmt.Errorf("get export: %w", err)
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

// ---- EDRM bundle build ---------------------------------------------------

const (
	datDelim = "\x14" // Concordance field delimiter (¶)
	datQual  = "\xfe" // Concordance text qualifier (þ)
)

func datRow(fields ...string) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteString(datDelim)
		}
		b.WriteString(datQual)
		b.WriteString(strings.ReplaceAll(f, datQual, ""))
		b.WriteString(datQual)
	}
	b.WriteString("\r\n")
	return b.String()
}

// buildEDRMZip writes the export bundle: manifest.json (+ HMAC sig), an EDRM
// Concordance DAT load file, and per-document metadata + audit-trail JSON.
func (s *DocumentService) buildEDRMZip(ctx context.Context, tenantID, userID uuid.UUID, w io.Writer, holdID uuid.UUID, query string, docIDs []uuid.UUID) error {
	items := make([]DiscoveryManifestItem, 0, len(docIDs))
	audits := make(map[string][]byte, len(docIDs))
	for _, docID := range docIDs {
		doc, _, err := s.GetDocument(ctx, docID)
		if err != nil {
			s.log.Warn().Err(err).Str("doc", docID.String()).Msg("ediscovery hold: skip")
			continue
		}
		it := DiscoveryManifestItem{
			DocumentID:     doc.ID.String(),
			Title:          doc.Title,
			LifecycleState: string(doc.LifecycleState),
			ContentSHA256:  doc.SHA256Hash,
			CreatedAt:      doc.CreatedAt,
		}
		if doc.CurrentVersionID != nil {
			it.CurrentVersion = doc.CurrentVersionID.String()
		}
		items = append(items, it)
		audits[doc.ID.String()] = s.fetchDocAudit(ctx, tenantID, docID)
	}

	manifest := &DiscoveryManifest{
		Version:    "1",
		CaseID:     "hold:" + holdID.String(),
		CaseName:   "Legal hold export",
		TenantID:   tenantID.String(),
		ExportedBy: userID.String(),
		ExportedAt: time.Now().UTC(),
		Documents:  items,
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipEntry(zw, "manifest.json", manifestBytes); err != nil {
		return err
	}
	if sig, err := hmacSign(manifestBytes, ediscoverySigningKey()); err == nil {
		_ = writeZipEntry(zw, "manifest.json.sig", []byte(sig))
	}

	// EDRM Concordance DAT load file — one row per document.
	var dat strings.Builder
	dat.WriteString(datRow("DOCID", "TITLE", "LIFECYCLE", "CONTENT_SHA256", "CREATED_AT", "NATIVE_PATH", "AUDIT_PATH"))
	for _, it := range items {
		dat.WriteString(datRow(
			it.DocumentID, it.Title, it.LifecycleState, it.ContentSHA256,
			it.CreatedAt.UTC().Format(time.RFC3339),
			"metadata/doc-"+it.DocumentID+".json",
			"audit/doc-"+it.DocumentID+".json",
		))
	}
	if err := writeZipEntry(zw, "loadfile.dat", []byte(dat.String())); err != nil {
		return err
	}

	for _, it := range items {
		raw, _ := json.MarshalIndent(it, "", "  ")
		if err := writeZipEntry(zw, "metadata/doc-"+it.DocumentID+".json", raw); err != nil {
			return err
		}
		if a := audits[it.DocumentID]; a != nil {
			if err := writeZipEntry(zw, "audit/doc-"+it.DocumentID+".json", a); err != nil {
				return err
			}
		}
	}
	return nil
}

// fetchDocAudit pulls a document's audit events into a JSON array for the
// bundle. Best-effort: a query error (e.g. audit_events not granted to the
// document role) yields an empty array rather than failing the export.
func (s *DocumentService) fetchDocAudit(ctx context.Context, tenantID, docID uuid.UUID) []byte {
	type ev struct {
		Action       string    `json:"action"`
		Actor        string    `json:"actor"`
		ActorName    string    `json:"actor_name,omitempty"`
		ResourceType string    `json:"resource_type"`
		CreatedAt    time.Time `json:"created_at"`
		EventHash    string    `json:"event_hash"`
	}
	events := []ev{}
	_ = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT action, actor::text, COALESCE(actor_name,''), COALESCE(resource_type,''),
			       created_at, COALESCE(event_hash,'')
			  FROM audit_events
			 WHERE tenant_id=$1 AND resource_id=$2
			 ORDER BY created_at ASC`, tenantID, docID)
		if err != nil {
			s.log.Warn().Err(err).Str("doc", docID.String()).Msg("ediscovery: audit fetch failed (empty trail)")
			return nil
		}
		defer rows.Close()
		for rows.Next() {
			var e ev
			if err := rows.Scan(&e.Action, &e.Actor, &e.ActorName, &e.ResourceType, &e.CreatedAt, &e.EventHash); err != nil {
				return nil
			}
			events = append(events, e)
		}
		return nil
	})
	out, _ := json.MarshalIndent(events, "", "  ")
	return out
}
