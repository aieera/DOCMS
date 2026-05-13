// Package intake implements §12.4 watched-folder ingestion (ADR 0088).
//
// A worker watches per-tenant directories. New files become documents
// in the tenant's configured workspace/folder; OCR + classify pipelines
// auto-fire because those subscribers already listen on
// dms.version.uploaded.v1 from the document service.
//
// Idempotency: (tenant_id, folder_id, sha256) is the unique key on
// intake_ingested_files. Re-dropping the same bytes lands once.
//
// Per-file flow:
//   1. fsnotify (or fallback 30s poll) reports a new path.
//   2. Wait 2s for the file to settle (scanners write in chunks).
//   3. SHA-256 + MIME detect.
//   4. INSERT ON CONFLICT DO NOTHING — duplicate drops move to
//      `processed/` and exit.
//   5. Call DocumentClient to create the document + version
//      (reuses the same client the email worker uses).
//   6. On success: move source file to `processed/`, stamp status
//      = "ingested" with the document_id.
//   7. On failure: move source file to `quarantine/`, stamp status
//      = "failed" with the error message.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/connector/internal/email"
)

// Folder is what the admin REST surface returns for one watched dir.
type Folder struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenant_id"`
	Label             string    `json:"label"`
	Active            bool      `json:"active"`
	HostPath          string    `json:"host_path"`
	TargetWorkspaceID string    `json:"target_workspace_id,omitempty"`
	TargetFolderID    string    `json:"target_folder_id,omitempty"`
	QuarantineSubdir  string    `json:"quarantine_subdir"`
	ProcessedSubdir   string    `json:"processed_subdir"`
	Recurse           bool      `json:"recurse"`
	ExtensionsCSV     string    `json:"extensions_csv,omitempty"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	FilesIngested     int64     `json:"files_ingested"`
	LastError         string    `json:"last_error,omitempty"`
	CreatedBy         string    `json:"created_by,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// CreateInput is the POST body shape.
type CreateInput struct {
	Label             string `json:"label"`
	HostPath          string `json:"host_path"`
	TargetWorkspaceID string `json:"target_workspace_id,omitempty"`
	TargetFolderID    string `json:"target_folder_id,omitempty"`
	QuarantineSubdir  string `json:"quarantine_subdir,omitempty"`
	ProcessedSubdir   string `json:"processed_subdir,omitempty"`
	Recurse           bool   `json:"recurse"`
	ExtensionsCSV     string `json:"extensions_csv,omitempty"`
}

// PatchInput is the PATCH body shape. All fields optional.
type PatchInput struct {
	Active            *bool   `json:"active,omitempty"`
	Label             *string `json:"label,omitempty"`
	TargetWorkspaceID *string `json:"target_workspace_id,omitempty"`
	TargetFolderID    *string `json:"target_folder_id,omitempty"`
	QuarantineSubdir  *string `json:"quarantine_subdir,omitempty"`
	ProcessedSubdir   *string `json:"processed_subdir,omitempty"`
	Recurse           *bool   `json:"recurse,omitempty"`
	ExtensionsCSV     *string `json:"extensions_csv,omitempty"`
}

// RecentFile is what /recent-files returns.
type RecentFile struct {
	ID           string    `json:"id"`
	SourcePath   string    `json:"source_path"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	DocumentID   string    `json:"document_id,omitempty"`
	IngestStatus string    `json:"ingest_status"`
	IngestError  string    `json:"ingest_error,omitempty"`
	IngestedAt   time.Time `json:"ingested_at"`
}

// Service orchestrates folder CRUD + the watcher loop.
type Service struct {
	pool *pgxpool.Pool
	docs *email.DocumentClient
	log  zerolog.Logger

	mu      sync.Mutex
	stopFns map[string]context.CancelFunc // folder_id → cancel for its watcher goroutine
}

// New constructs a Service.
func New(pool *pgxpool.Pool, docs *email.DocumentClient, log zerolog.Logger) *Service {
	return &Service{
		pool:    pool,
		docs:    docs,
		log:     log,
		stopFns: map[string]context.CancelFunc{},
	}
}

// Start kicks off the supervisor loop. Every 60s it loads the active
// configs, starts watchers for ones that don't have one, and stops
// watchers whose config disappeared / went inactive.
func (s *Service) Start(parent context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	s.reconcile(parent)
	for {
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
			s.reconcile(parent)
		}
	}
}

func (s *Service) reconcile(parent context.Context) {
	folders, err := s.listActiveFolders(parent)
	if err != nil {
		s.log.Error().Err(err).Msg("intake: list active folders")
		return
	}
	want := map[string]Folder{}
	for _, f := range folders {
		want[f.ID] = f
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Start watchers for newly-active folders.
	for id, f := range want {
		if _, running := s.stopFns[id]; running {
			continue
		}
		ctx, cancel := context.WithCancel(parent)
		s.stopFns[id] = cancel
		go s.watch(ctx, f)
	}
	// Stop watchers for folders that vanished.
	for id, cancel := range s.stopFns {
		if _, still := want[id]; !still {
			cancel()
			delete(s.stopFns, id)
		}
	}
}

// watch tails one folder until ctx is cancelled. Falls back to 30s
// polling if fsnotify is unavailable (e.g. inotify-watcher exhausted).
func (s *Service) watch(ctx context.Context, f Folder) {
	if err := ensureSubdirs(f); err != nil {
		s.recordError(ctx, f, "ensure subdirs: "+err.Error())
		return
	}
	s.log.Info().Str("folder_id", f.ID).Str("path", f.HostPath).Bool("recurse", f.Recurse).Msg("intake watcher started")

	// Initial sweep so files dropped before the watcher started don't
	// languish forever.
	s.sweep(ctx, f)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Warn().Err(err).Str("path", f.HostPath).Msg("intake: fsnotify unavailable, falling back to 30s poll")
		s.pollLoop(ctx, f)
		return
	}
	defer w.Close()

	if err := s.addPath(w, f); err != nil {
		s.log.Warn().Err(err).Str("path", f.HostPath).Msg("intake: watcher add failed, falling back to poll")
		s.pollLoop(ctx, f)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			// If a new subdir appeared and we're recursing, add it.
			if info, err := os.Stat(ev.Name); err == nil && info.IsDir() && f.Recurse {
				_ = w.Add(ev.Name)
				continue
			}
			s.handleFile(ctx, f, ev.Name)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.log.Warn().Err(err).Str("folder_id", f.ID).Msg("intake: watcher error")
		}
	}
}

func (s *Service) addPath(w *fsnotify.Watcher, f Folder) error {
	if err := w.Add(f.HostPath); err != nil {
		return err
	}
	if !f.Recurse {
		return nil
	}
	return filepath.Walk(f.HostPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		// Don't recurse into our own quarantine/processed subdirs.
		base := filepath.Base(path)
		if base == f.ProcessedSubdir || base == f.QuarantineSubdir {
			return filepath.SkipDir
		}
		_ = w.Add(path)
		return nil
	})
}

func (s *Service) pollLoop(ctx context.Context, f Folder) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx, f)
		}
	}
}

// sweep walks the host_path once and processes every file it finds.
// Used for both initial-boot catchup and the polling fallback.
func (s *Service) sweep(ctx context.Context, f Folder) {
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(filepath.Dir(path))
		if base == f.ProcessedSubdir || base == f.QuarantineSubdir {
			return nil
		}
		s.handleFile(ctx, f, path)
		return nil
	}
	if f.Recurse {
		_ = filepath.Walk(f.HostPath, walkFn)
		return
	}
	entries, err := os.ReadDir(f.HostPath)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(f.HostPath, e.Name())
		info, statErr := os.Stat(full)
		if statErr != nil {
			continue
		}
		_ = walkFn(full, info, nil)
	}
}

// handleFile is the per-file pipeline.
func (s *Service) handleFile(ctx context.Context, f Folder, path string) {
	// 1) Skip files we obviously shouldn't pick up: hidden / temp / our own subdirs.
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") {
		return
	}
	parent := filepath.Base(filepath.Dir(path))
	if parent == f.ProcessedSubdir || parent == f.QuarantineSubdir {
		return
	}
	if !s.extensionAllowed(name, f.ExtensionsCSV) {
		return
	}

	// 2) Wait for the file to settle (scanner mid-write).
	if !s.fileSettled(path) {
		return
	}

	// 3) SHA-256 + size + MIME.
	sha, size, err := hashFile(path)
	if err != nil {
		return // file vanished between scan + open
	}
	mime, _ := detectMIME(path)
	if mime == "" {
		mime = "application/octet-stream"
	}

	// 4) INSERT ON CONFLICT — idempotency gate.
	rowID, isDup, err := s.recordPending(ctx, f, path, sha, size)
	if err != nil {
		s.log.Error().Err(err).Str("path", path).Msg("intake: record pending")
		return
	}
	if isDup {
		// Duplicate: move to processed and exit.
		_ = s.move(f, path, f.ProcessedSubdir)
		return
	}

	// 5) Materialise the document.
	bytes, err := os.ReadFile(path)
	if err != nil {
		s.markFailed(ctx, f, rowID, "read: "+err.Error())
		_ = s.move(f, path, f.QuarantineSubdir)
		return
	}
	docID, attachIDs, mErr := s.docs.MaterialiseFile(ctx, f.TenantID, f.CreatedBy, f.TargetWorkspaceID, f.TargetFolderID,
		name, mime, bytes, map[string]any{
			"intake.source":       "drop_folder",
			"intake.folder_id":    f.ID,
			"intake.source_path":  path,
			"intake.sha256":       sha,
		})
	_ = attachIDs // drop-folder ingestion is single-file; no children.
	if mErr != nil {
		s.markFailed(ctx, f, rowID, "materialise: "+mErr.Error())
		_ = s.move(f, path, f.QuarantineSubdir)
		return
	}

	// 6) Success: stamp the row + move source.
	s.markIngested(ctx, f, rowID, docID)
	_ = s.move(f, path, f.ProcessedSubdir)
}

// fileSettled waits up to 4s for the file's size to stop growing.
// Returns false if it's still changing — caller skips this round.
func (s *Service) fileSettled(path string) bool {
	prev, err := os.Stat(path)
	if err != nil {
		return false
	}
	time.Sleep(2 * time.Second)
	cur, err := os.Stat(path)
	if err != nil {
		return false
	}
	if cur.Size() != prev.Size() {
		return false
	}
	return true
}

func (s *Service) extensionAllowed(name, csv string) bool {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	for _, e := range strings.Split(csv, ",") {
		if strings.EqualFold(strings.TrimSpace(e), ext) {
			return true
		}
	}
	return false
}

func (s *Service) move(f Folder, src, subdir string) error {
	dst := filepath.Join(f.HostPath, subdir, filepath.Base(src))
	// If a file with the same name already exists in dst, append a
	// suffix so we don't overwrite history.
	if _, err := os.Stat(dst); err == nil {
		dst = dst + "." + time.Now().UTC().Format("20060102T150405")
	}
	return os.Rename(src, dst)
}

func ensureSubdirs(f Folder) error {
	for _, sub := range []string{f.ProcessedSubdir, f.QuarantineSubdir} {
		if err := os.MkdirAll(filepath.Join(f.HostPath, sub), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func detectMIME(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	buf := make([]byte, 512)
	n, _ := fh.Read(buf)
	return http.DetectContentType(buf[:n]), nil
}

// ---- DB helpers -----------------------------------------------------

func (s *Service) listActiveFolders(ctx context.Context) ([]Folder, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, tenant_id::text, label, active, host_path,
		       COALESCE(target_workspace_id::text, ''),
		       COALESCE(target_folder_id::text, ''),
		       quarantine_subdir, processed_subdir, recurse, extensions_csv,
		       last_seen_at, files_ingested, COALESCE(last_error, ''),
		       COALESCE(created_by::text, ''), created_at
		  FROM intake_drop_folders WHERE active = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Folder
	for rows.Next() {
		f := Folder{}
		if err := rows.Scan(&f.ID, &f.TenantID, &f.Label, &f.Active, &f.HostPath,
			&f.TargetWorkspaceID, &f.TargetFolderID,
			&f.QuarantineSubdir, &f.ProcessedSubdir, &f.Recurse, &f.ExtensionsCSV,
			&f.LastSeenAt, &f.FilesIngested, &f.LastError,
			&f.CreatedBy, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Service) recordPending(ctx context.Context, f Folder, path, sha string, size int64) (string, bool, error) {
	tenantUUID, err := uuid.Parse(f.TenantID)
	if err != nil {
		return "", false, err
	}
	rel, _ := filepath.Rel(f.HostPath, path)
	if rel == "" {
		rel = filepath.Base(path)
	}
	rowID := newUUID()
	folderUUID, err := uuid.Parse(f.ID)
	if err != nil {
		return "", false, err
	}
	var isDup bool
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO intake_ingested_files
			    (tenant_id, id, folder_id, source_path, sha256, size_bytes, ingest_status)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending')
			ON CONFLICT (tenant_id, folder_id, sha256) DO NOTHING`,
			f.TenantID, rowID, folderUUID, rel, sha, size)
		if err != nil {
			return err
		}
		isDup = tag.RowsAffected() == 0
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return rowID, isDup, nil
}

func (s *Service) markIngested(ctx context.Context, f Folder, rowID, docID string) {
	tenantUUID, err := uuid.Parse(f.TenantID)
	if err != nil {
		return
	}
	_ = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE intake_ingested_files
			    SET ingest_status = 'ingested', document_id = $3::uuid
			  WHERE tenant_id = $1 AND id = $2`,
			f.TenantID, rowID, docID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE intake_drop_folders
			    SET files_ingested = files_ingested + 1, last_seen_at = now(), last_error = NULL
			  WHERE tenant_id = $1 AND id = $2`,
			f.TenantID, f.ID)
		return err
	})
}

func (s *Service) markFailed(ctx context.Context, f Folder, rowID, msg string) {
	tenantUUID, err := uuid.Parse(f.TenantID)
	if err != nil {
		return
	}
	_ = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE intake_ingested_files
			    SET ingest_status = 'failed', ingest_error = $3
			  WHERE tenant_id = $1 AND id = $2`,
			f.TenantID, rowID, msg)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE intake_drop_folders SET last_error = $3, last_seen_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			f.TenantID, f.ID, msg)
		return err
	})
}

func (s *Service) recordError(ctx context.Context, f Folder, msg string) {
	s.log.Warn().Str("folder_id", f.ID).Str("err", msg).Msg("intake")
	_, _ = s.pool.Exec(ctx,
		`UPDATE intake_drop_folders SET last_error = $3, last_seen_at = now()
		  WHERE tenant_id = $1 AND id = $2`,
		f.TenantID, f.ID, msg)
}

// ---- public REST surface --------------------------------------------

// CreateFolder persists a new config.
func (s *Service) CreateFolder(ctx context.Context, tenantID, actorID string, in CreateInput) (*Folder, error) {
	if in.Label == "" {
		return nil, errors.New("label required")
	}
	if in.HostPath == "" {
		return nil, errors.New("host_path required")
	}
	if !filepath.IsAbs(in.HostPath) {
		return nil, errors.New("host_path must be absolute")
	}
	if in.QuarantineSubdir == "" {
		in.QuarantineSubdir = "quarantine"
	}
	if in.ProcessedSubdir == "" {
		in.ProcessedSubdir = "processed"
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	id := newUUID()
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		var actor any
		if actorID != "" {
			actor = actorID
		}
		var ws, fd any
		if in.TargetWorkspaceID != "" {
			ws = in.TargetWorkspaceID
		}
		if in.TargetFolderID != "" {
			fd = in.TargetFolderID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO intake_drop_folders
			    (tenant_id, id, label, host_path,
			     target_workspace_id, target_folder_id,
			     quarantine_subdir, processed_subdir, recurse, extensions_csv,
			     created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tenantID, id, in.Label, in.HostPath,
			ws, fd,
			in.QuarantineSubdir, in.ProcessedSubdir, in.Recurse, in.ExtensionsCSV,
			actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.getFolder(ctx, tenantID, id)
}

// ListFolders returns all configs for a tenant.
func (s *Service) ListFolders(ctx context.Context, tenantID string) ([]Folder, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	var out []Folder
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, label, active, host_path,
			       COALESCE(target_workspace_id::text, ''),
			       COALESCE(target_folder_id::text, ''),
			       quarantine_subdir, processed_subdir, recurse, extensions_csv,
			       last_seen_at, files_ingested, COALESCE(last_error, ''),
			       COALESCE(created_by::text, ''), created_at
			  FROM intake_drop_folders WHERE tenant_id = $1
			  ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f := Folder{TenantID: tenantID}
			if err := rows.Scan(&f.ID, &f.Label, &f.Active, &f.HostPath,
				&f.TargetWorkspaceID, &f.TargetFolderID,
				&f.QuarantineSubdir, &f.ProcessedSubdir, &f.Recurse, &f.ExtensionsCSV,
				&f.LastSeenAt, &f.FilesIngested, &f.LastError,
				&f.CreatedBy, &f.CreatedAt); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	return out, err
}

// PatchFolder applies a partial update.
func (s *Service) PatchFolder(ctx context.Context, tenantID, id string, in PatchInput) (*Folder, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE intake_drop_folders SET
			    active             = COALESCE($3, active),
			    label              = COALESCE($4, label),
			    target_workspace_id= COALESCE($5::uuid, target_workspace_id),
			    target_folder_id   = COALESCE($6::uuid, target_folder_id),
			    quarantine_subdir  = COALESCE($7, quarantine_subdir),
			    processed_subdir   = COALESCE($8, processed_subdir),
			    recurse            = COALESCE($9, recurse),
			    extensions_csv     = COALESCE($10, extensions_csv),
			    updated_at         = now()
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
			in.Active, in.Label, in.TargetWorkspaceID, in.TargetFolderID,
			in.QuarantineSubdir, in.ProcessedSubdir, in.Recurse, in.ExtensionsCSV)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.getFolder(ctx, tenantID, id)
}

// DeleteFolder tombstones a config (sets active=false).
func (s *Service) DeleteFolder(ctx context.Context, tenantID, id string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE intake_drop_folders SET active = FALSE, updated_at = now()
			  WHERE tenant_id = $1 AND id = $2`, tenantID, id)
		return err
	})
}

// ScanNow does a one-off sweep of the folder. Useful when fsnotify is
// flaky or after a downtime catchup is needed.
func (s *Service) ScanNow(ctx context.Context, tenantID, id string) error {
	f, err := s.getFolder(ctx, tenantID, id)
	if err != nil || f == nil {
		if err == nil {
			return errors.New("not found")
		}
		return err
	}
	s.sweep(ctx, *f)
	return nil
}

// RecentFiles returns the last 50 ingestion attempts for the folder.
func (s *Service) RecentFiles(ctx context.Context, tenantID, id string) ([]RecentFile, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	var out []RecentFile
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, source_path, sha256, size_bytes,
			       COALESCE(document_id::text, ''), ingest_status,
			       COALESCE(ingest_error, ''), ingested_at
			  FROM intake_ingested_files
			 WHERE tenant_id = $1 AND folder_id = $2
			 ORDER BY ingested_at DESC LIMIT 50`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := RecentFile{}
			if err := rows.Scan(&r.ID, &r.SourcePath, &r.SHA256, &r.SizeBytes,
				&r.DocumentID, &r.IngestStatus, &r.IngestError, &r.IngestedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) getFolder(ctx context.Context, tenantID, id string) (*Folder, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	f := &Folder{TenantID: tenantID}
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text, label, active, host_path,
			       COALESCE(target_workspace_id::text, ''),
			       COALESCE(target_folder_id::text, ''),
			       quarantine_subdir, processed_subdir, recurse, extensions_csv,
			       last_seen_at, files_ingested, COALESCE(last_error, ''),
			       COALESCE(created_by::text, ''), created_at
			  FROM intake_drop_folders WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&f.ID, &f.Label, &f.Active, &f.HostPath,
			&f.TargetWorkspaceID, &f.TargetFolderID,
			&f.QuarantineSubdir, &f.ProcessedSubdir, &f.Recurse, &f.ExtensionsCSV,
			&f.LastSeenAt, &f.FilesIngested, &f.LastError,
			&f.CreatedBy, &f.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

// ---- helpers --------------------------------------------------------

func newUUID() string {
	id := uuid.New()
	return id.String()
}

// silence unused
var _ = fmt.Sprintf