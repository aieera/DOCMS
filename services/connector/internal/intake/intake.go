// Package intake implements §12.4 watched-folder ingestion (ADR 0088).
//
// A worker watches per-tenant directories. New files become documents
// in the tenant's configured workspace/folder; OCR + classify pipelines
// auto-fire because those subscribers already listen on
// dms.version.uploaded.v1 from the document service.
//
// Idempotency: (tenant_id, folder_id, sha256) is the unique key on
// intake_ingested_files. Re-dropping bytes that were already INGESTED lands
// once; re-dropping bytes whose earlier attempt failed retries (N-3).
//
// Per-file flow:
//  1. fsnotify (or the 30s poll / 5 min safety sweep) reports a new path.
//  2. Only a regular file whose directory resolves inside host_path is
//     considered; wait for it to settle (scanners write in chunks).
//  3. Claim it by renaming to <name>.ingesting — atomic, so concurrent
//     sweeps (or replicas sharing the volume) process each file once. A
//     claim older than the 15-minute lease is released by the next sweep.
//  4. Read it once (no-follow, size-capped); SHA-256 + MIME from those bytes.
//  5. Upsert the row: a new row, or an existing failed/quarantined row (or
//     a pending one whose lease expired), is claimed for ingest; an
//     existing ingested row is a duplicate (moved to `processed/`); a
//     pending row inside its lease is someone else's in-flight ingest (the
//     file stays claimed and is retried after the lease).
//  6. Call the shared ingest pipeline as the folder's creator with their
//     current role (the same client the email worker + Drive import use),
//     which fires dms.version.uploaded.v1 → OCR + embed + index.
//  7. On success: move the file to `processed/`, stamp status "ingested"
//     with the document_id. On failure: move it to `quarantine/`, stamp
//     status "failed" with the error message.
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
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/connector/internal/ingest"
)

// claimSuffix marks a file a worker has claimed for ingest.
const claimSuffix = ".ingesting"

// claimLease bounds how long a claim (file rename + pending row) is
// honored before another worker may take it over (crash recovery).
const claimLease = 15 * time.Minute

// safetySweepInterval re-sweeps fsnotify-watched folders so missed events
// and stale claims are eventually picked up.
const safetySweepInterval = 5 * time.Minute

// Folder is what the admin REST surface returns for one watched dir.
type Folder struct {
	ID                string     `json:"id"`
	TenantID          string     `json:"tenant_id"`
	Label             string     `json:"label"`
	Active            bool       `json:"active"`
	HostPath          string     `json:"host_path"`
	TargetWorkspaceID string     `json:"target_workspace_id,omitempty"`
	TargetFolderID    string     `json:"target_folder_id,omitempty"`
	QuarantineSubdir  string     `json:"quarantine_subdir"`
	ProcessedSubdir   string     `json:"processed_subdir"`
	Recurse           bool       `json:"recurse"`
	ExtensionsCSV     string     `json:"extensions_csv,omitempty"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	FilesIngested     int64      `json:"files_ingested"`
	LastError         string     `json:"last_error,omitempty"`
	CreatedBy         string     `json:"created_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
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
	pool   *pgxpool.Pool
	ingest ingester
	log    zerolog.Logger
	// settleDelay is how long a file's size must stay put before pickup.
	settleDelay time.Duration
	// maxFileBytes caps one dropped file (N-9); larger files are
	// quarantined unread.
	maxFileBytes int64
	// root is SEDOC_INTAKE_ROOT; tenant t may only watch <root>/<t>/...
	root string

	mu      sync.Mutex
	stopFns map[string]context.CancelFunc // folder_id → cancel for its watcher goroutine
}

// New constructs a Service. ingestClient may be nil (intake then records a
// failure per file instead of materialising) — main.go passes a live client
// when storage is reachable.
func New(pool *pgxpool.Pool, ingestClient *ingest.Client, log zerolog.Logger) *Service {
	s := &Service{
		pool:         pool,
		log:          log,
		root:         intakeRootFromEnv(),
		settleDelay:  2 * time.Second,
		maxFileBytes: DefaultIntakeMaxFileBytes,
		stopFns:      map[string]context.CancelFunc{},
	}
	// Keep a nil interface (not one wrapping a nil pointer) when storage is
	// unreachable, so the per-file nil guard still fires.
	if ingestClient != nil {
		s.ingest = ingestClient
	}
	return s
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
	if err := s.watchable(&f); err != nil {
		s.recordError(ctx, f, "not watched: "+err.Error())
		return
	}
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

	safety := time.NewTicker(safetySweepInterval)
	defer safety.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-safety.C:
			s.sweep(ctx, f)
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			// If a new subdir appeared and we're recursing, add it.
			// Lstat: a symlinked directory is never added (N-6).
			if info, err := os.Lstat(ev.Name); err == nil && info.IsDir() && f.Recurse {
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
	// Re-check confinement on every pass, not only at watcher start: the
	// watched directory (or a parent) may have been swapped for a symlink
	// out of the tenant root since then (H-4).
	if err := s.watchable(&f); err != nil {
		s.log.Warn().Err(err).Str("folder_id", f.ID).Str("path", f.HostPath).Msg("intake: folder no longer confined; sweep skipped")
		return
	}
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(filepath.Dir(path))
		if base == f.ProcessedSubdir || base == f.QuarantineSubdir {
			return nil
		}
		if strings.HasSuffix(path, claimSuffix) {
			// A claim older than the lease belongs to a worker that died
			// mid-ingest: release it so this pass retries the file.
			if time.Since(info.ModTime()) <= claimLease {
				return nil
			}
			orig := strings.TrimSuffix(path, claimSuffix)
			// A failed release skips this file for the pass (the next sweep
			// retries); the path comes from the operator-confined intake root.
			if err := os.Rename(path, orig); err != nil { //nolint:gosec // G122: path is under the operator-confined intake root; a raced rename only skips this entry
				return nil //nolint:nilerr // walk callback skips the entry on purpose; the next sweep retries
			}
			path = orig
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
		info, statErr := os.Lstat(full)
		if statErr != nil {
			continue
		}
		_ = walkFn(full, info, nil)
	}
}

// handleFile is the per-file pipeline.
func (s *Service) handleFile(ctx context.Context, f Folder, path string) {
	// 1) Skip files we obviously shouldn't pick up: hidden / temp / claimed
	//    / our own subdirs.
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, claimSuffix) {
		return
	}
	parent := filepath.Base(filepath.Dir(path))
	if parent == f.ProcessedSubdir || parent == f.QuarantineSubdir {
		return
	}
	if !s.extensionAllowed(name, f.ExtensionsCSV) {
		return
	}
	// Only regular files that really live under host_path (N-6): no
	// symlinks, FIFOs, devices or sockets, and no path through a symlinked
	// directory.
	if !s.regularUnder(&f, path) {
		return
	}

	// 2) Wait for the file to settle (scanner mid-write).
	if !s.fileSettled(path) {
		return
	}

	// 3) Claim: exactly one worker wins the rename; the rest see ENOENT.
	claimed := path + claimSuffix
	if err := os.Rename(path, claimed); err != nil {
		return
	}
	now := time.Now()
	_ = os.Chtimes(claimed, now, now) // claim time → lease clock for recovery

	// 4) Read once through a no-follow, non-blocking descriptor (the file
	//    could have been swapped for a link or FIFO since the check above),
	//    then SHA-256 + size + MIME from those bytes.
	bytes, err := readRegular(claimed, s.maxFileBytes)
	if errors.Is(err, errFileTooLarge) {
		s.recordError(ctx, f, fmt.Sprintf("%s: %v; quarantined", name, err))
		_ = s.move(&f, claimed, name, f.QuarantineSubdir)
		return
	}
	if err != nil {
		// Not readable as a regular file (permissions, or swapped for a
		// link/FIFO after the check): quarantine it once, with the reason on
		// the folder, instead of leaving the claim to be released and
		// re-claimed every lease period. Renaming moves a link itself, never
		// its target.
		s.recordError(ctx, f, fmt.Sprintf("%s: not readable as a regular file (%v); quarantined", name, err))
		_ = s.move(&f, claimed, name, f.QuarantineSubdir)
		return
	}
	sum := sha256.Sum256(bytes)
	sha := hex.EncodeToString(sum[:])
	size := int64(len(bytes))
	mime := http.DetectContentType(bytes[:min(len(bytes), 512)])

	// 5) Row claim — idempotency gate.
	// On a DB error or a live claim elsewhere the file stays claimed: the
	// safety sweep releases it once claimLease passes. Renaming it back
	// now would re-fire fsnotify and spin every settle interval.
	rowID, outcome, err := s.claimRow(ctx, &f, path, sha, size)
	if err != nil {
		s.log.Error().Err(err).Str("path", path).Msg("intake: claim row; retried after the claim lease")
		return
	}
	switch outcome {
	case claimDuplicate:
		_ = s.move(&f, claimed, name, f.ProcessedSubdir)
		return
	case claimBusy:
		// Same bytes are being ingested by another worker right now.
		return
	case claimIngest:
		// We own the row: materialize below.
	}

	// 6) Materialize the document.
	if s.ingest == nil {
		s.markFailed(ctx, f, rowID, "ingest client unavailable (storage unreachable)")
		_ = s.move(&f, claimed, name, f.QuarantineSubdir)
		return
	}
	// Full ingest: create document + upload blob + version, which fires
	// dms.version.uploaded.v1 → OCR + embed + index. Runs as the folder's
	// creator (f.CreatedBy, with their current role) via internal-service
	// auth (empty authToken).
	docID, mErr := s.ingest.IngestFile(ctx, f.TenantID, f.CreatedBy, "",
		f.TargetWorkspaceID, f.TargetFolderID, name, mime, bytes, map[string]any{
			"intake.source":      "drop_folder",
			"intake.folder_id":   f.ID,
			"intake.source_path": path,
			"intake.sha256":      sha,
		})
	if mErr != nil {
		s.markFailed(ctx, f, rowID, "ingest: "+mErr.Error())
		_ = s.move(&f, claimed, name, f.QuarantineSubdir)
		return
	}

	// 7) Success: stamp the row + move source.
	s.markIngested(ctx, f, rowID, docID)
	_ = s.move(&f, claimed, name, f.ProcessedSubdir)
}

// fileSettled waits up to 4s for the file's size to stop growing.
// Returns false if it's still changing — caller skips this round.
func (s *Service) fileSettled(path string) bool {
	prev, err := os.Stat(path)
	if err != nil {
		return false
	}
	time.Sleep(s.settleDelay)
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

// move files src (possibly a claimed <name>.ingesting path) into subdir
// under its original name.
func (s *Service) move(f *Folder, src, name, subdir string) error {
	dst := filepath.Join(f.HostPath, subdir, name)
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

// regularUnder reports whether path is a regular file (not a symlink,
// FIFO, device or socket) whose directory, with every symlink resolved, is
// inside both the tenant's resolved intake root and the resolved
// f.HostPath. Checking the tenant root (not just host_path) is what stops a
// host_path swapped for a symlink to e.g. /run/secrets after the watcher
// started: the swapped path resolves outside the root (H-4).
func (s *Service) regularUnder(f *Folder, path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	tr, err := s.tenantRoot(f.TenantID)
	if err != nil {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(tr)
	if err != nil {
		return false
	}
	realHost, err := filepath.EvalSymlinks(f.HostPath)
	if err != nil {
		return false
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false
	}
	return within(realRoot, realDir) && within(realHost, realDir)
}

// DefaultIntakeMaxFileBytes caps a dropped file: it is read into memory to
// hash and upload, so anything larger is quarantined unread.
const DefaultIntakeMaxFileBytes = 256 << 20

var errFileTooLarge = errors.New("file exceeds the intake size cap")

// readRegular opens path without following a final symlink and without
// blocking on a FIFO, re-checks on the descriptor that it is a regular
// file within maxBytes (fstat before reading, N-9), and reads at most
// maxBytes.
func readRegular(path string, maxBytes int64) ([]byte, error) {
	fh, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if maxBytes > 0 && fi.Size() > maxBytes {
		return nil, fmt.Errorf("%w (%d > %d bytes)", errFileTooLarge, fi.Size(), maxBytes)
	}
	limit := maxBytes
	if limit <= 0 {
		limit = DefaultIntakeMaxFileBytes
	}
	data, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (grew past %d bytes while reading)", errFileTooLarge, limit)
	}
	return data, nil
}

// ---- DB helpers -----------------------------------------------------

// listActiveFolders is legitimately cross-tenant (the reconcile loop
// watches every tenant's drop folders), so it uses the sanctioned
// enumerate-tenants shape (Wave A.1, issue #71): the former global
// scan returned 0 rows under prod NOBYPASSRLS and intake silently
// stopped watching everything.
func (s *Service) listActiveFolders(ctx context.Context) ([]Folder, error) {
	var out []Folder
	err := database.ForEachTenant(ctx, s.pool, func(tenantID uuid.UUID, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, tenant_id::text, label, active, host_path,
			       COALESCE(target_workspace_id::text, ''),
			       COALESCE(target_folder_id::text, ''),
			       quarantine_subdir, processed_subdir, recurse, extensions_csv,
			       last_seen_at, files_ingested, COALESCE(last_error, ''),
			       COALESCE(created_by::text, ''), created_at
			  FROM intake_drop_folders WHERE tenant_id = $1 AND active = TRUE`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f := Folder{}
			if err := rows.Scan(&f.ID, &f.TenantID, &f.Label, &f.Active, &f.HostPath,
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
	if err != nil {
		return nil, err
	}
	return out, nil
}

// claimOutcome is what the row claim decided for one file.
type claimOutcome int

const (
	claimIngest    claimOutcome = iota // this worker ingests (new or retryable row)
	claimDuplicate                     // the content is already ingested
	claimBusy                          // another worker holds a live claim
)

// claimRow upserts the (tenant, folder, sha256) row. A conflict is a
// duplicate only when the existing row is 'ingested' (N-3: a 'failed' row
// used to be treated as done, so a re-drop after a failure was filed to
// processed/ and never ingested). failed/quarantined rows, and pending rows
// whose lease (ingested_at) expired, are re-claimed; a pending row inside
// its lease is another worker's in-flight ingest.
func (s *Service) claimRow(ctx context.Context, f *Folder, path, sha string, size int64) (string, claimOutcome, error) {
	tenantUUID, err := uuid.Parse(f.TenantID)
	if err != nil {
		return "", 0, err
	}
	folderUUID, err := uuid.Parse(f.ID)
	if err != nil {
		return "", 0, err
	}
	rel, _ := filepath.Rel(f.HostPath, path)
	if rel == "" {
		rel = filepath.Base(path)
	}
	var (
		rowID   string
		outcome claimOutcome
	)
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `
			INSERT INTO intake_ingested_files
			    (tenant_id, id, folder_id, source_path, sha256, size_bytes, ingest_status, ingested_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'pending', now())
			ON CONFLICT (tenant_id, folder_id, sha256) DO UPDATE
			   SET ingest_status = 'pending',
			       ingest_error  = NULL,
			       source_path   = EXCLUDED.source_path,
			       size_bytes    = EXCLUDED.size_bytes,
			       ingested_at   = now()
			 WHERE intake_ingested_files.ingest_status IN ('failed', 'quarantined')
			    OR (intake_ingested_files.ingest_status = 'pending'
			        AND intake_ingested_files.ingested_at < now() - make_interval(secs => $7))
			RETURNING id::text`,
			f.TenantID, newUUID(), folderUUID, rel, sha, size, claimLease.Seconds(),
		).Scan(&rowID)
		if qerr == nil {
			outcome = claimIngest
			return nil
		}
		if !errors.Is(qerr, pgx.ErrNoRows) {
			return qerr
		}
		var status string
		if queryErr := tx.QueryRow(ctx, `
			SELECT ingest_status FROM intake_ingested_files
			 WHERE tenant_id = $1 AND folder_id = $2 AND sha256 = $3`,
			f.TenantID, folderUUID, sha).Scan(&status); queryErr != nil {
			return queryErr
		}
		if status == "ingested" {
			outcome = claimDuplicate
		} else {
			outcome = claimBusy
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return rowID, outcome, nil
}

func (s *Service) markIngested(ctx context.Context, f Folder, rowID, docID string) {
	tenantUUID, err := uuid.Parse(f.TenantID)
	if err != nil {
		return
	}
	_ = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE intake_ingested_files
			    SET ingest_status = 'ingested', document_id = $3::uuid, ingested_at = now()
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
	tid, err := uuid.Parse(f.TenantID)
	if err != nil {
		s.log.Error().Err(err).Str("folder_id", f.ID).Msg("intake: record error tenant parse")
		return
	}
	if err := database.WithTenantTx(ctx, s.pool, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE intake_drop_folders SET last_error = $3, last_seen_at = now()
			  WHERE tenant_id = $1 AND id = $2`,
			f.TenantID, f.ID, msg)
		return err
	}); err != nil {
		s.log.Error().Err(err).Str("folder_id", f.ID).Msg("intake: record error stamp failed")
	}
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
	hostPath, err := s.confinedHostPath(tenantID, in.HostPath)
	if err != nil {
		return nil, err
	}
	in.HostPath = hostPath
	if in.QuarantineSubdir == "" {
		in.QuarantineSubdir = "quarantine"
	}
	if in.ProcessedSubdir == "" {
		in.ProcessedSubdir = "processed"
	}
	for _, sub := range []string{in.QuarantineSubdir, in.ProcessedSubdir} {
		if validErr := validSubdir(sub); validErr != nil {
			return nil, validErr
		}
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return nil, err
	}
	id := newUUID()
	err = database.WithTenantTx(ctx, s.pool, tenantUUID, func(tx pgx.Tx) error {
		// Serialize folder creation per tenant so the overlap check below
		// and the insert cannot interleave with a concurrent create.
		if _, execErr := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('intake_drop_folders:' || $1::text))`, tenantID); execErr != nil {
			return execErr
		}
		if checkErr := checkNoOverlap(ctx, tx, tenantID, in.HostPath); checkErr != nil {
			return checkErr
		}
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

// checkNoOverlap rejects hostPath when it equals, lies inside, or contains
// the host_path of any of the tenant's folders, active or not (an inactive
// one can be re-activated). Paths are compared both lexically and with
// symlinks in their existing prefix resolved, so an overlap reached through
// a link inside the tenant root is caught too. Cross-tenant overlap is
// impossible by confinement (each tenant lives under
// <SEDOC_INTAKE_ROOT>/<tenant_id>); connector migration 000002 also adds a
// global host_path unique index.
func checkNoOverlap(ctx context.Context, tx pgx.Tx, tenantID, hostPath string) error {
	resolvedNew, err := resolveExisting(hostPath)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT host_path FROM intake_drop_folders WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		existing = filepath.Clean(existing)
		if within(existing, hostPath) || within(hostPath, existing) {
			return fmt.Errorf("%w: %s", ErrHostPathOverlap, existing)
		}
		resolvedOld, err := resolveExisting(existing)
		if err != nil {
			continue // unresolvable legacy path: the lexical check stands
		}
		if within(resolvedOld, resolvedNew) || within(resolvedNew, resolvedOld) {
			return fmt.Errorf("%w: %s", ErrHostPathOverlap, existing)
		}
	}
	return rows.Err()
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
	for _, sub := range []*string{in.QuarantineSubdir, in.ProcessedSubdir} {
		if sub == nil {
			continue
		}
		if err := validSubdir(*sub); err != nil {
			return nil, err
		}
	}
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
	if err := s.watchable(f); err != nil {
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
