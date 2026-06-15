// Package store is the integration worker's own Postgres state: the customer →
// folder map, the sync_log (whose row id IS the SeDoc Idempotency-Key seed), the
// shared bucket-folder registry, and ingestion tracking.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the worker's connection pool.
type Store struct{ pool *pgxpool.Pool }

// New constructs a Store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ErrNotFound is returned by lookups when no row exists.
var ErrNotFound = errors.New("not found")

// CustomerMapping is the resolved SeDoc folder layout for a customer.
type CustomerMapping struct {
	CustomerRef    string
	Name           string
	BucketFolderID string
	MainFolderID   string
	SubfolderIDs   map[string]string // quote/po/so/do/invoice/attachments → folder id
}

// SyncJob is one claimed unit of work.
type SyncJob struct {
	ID          uuid.UUID
	ERPEventID  string
	Kind        string
	CustomerRef string
	Attempts    int
	Payload     []byte // raw event JSON
}

// InsertEvent records an ERP event idempotently on erp_event_id. Returns the row
// id (the Idempotency-Key seed) and whether it was newly inserted — a duplicate
// webhook delivery returns isNew=false and is dropped by the caller.
func (s *Store) InsertEvent(ctx context.Context, erpEventID, kind, customerRef string, payload []byte) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sync_log (erp_event_id, kind, customer_ref, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (erp_event_id) DO NOTHING
		RETURNING id`, erpEventID, kind, customerRef, payload).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}
	// Conflict — return the existing row id.
	if serr := s.pool.QueryRow(ctx,
		`SELECT id FROM sync_log WHERE erp_event_id = $1`, erpEventID).Scan(&id); serr != nil {
		return uuid.Nil, false, serr
	}
	return id, false, nil
}

// ClaimDue leases up to limit due pending jobs (FOR UPDATE SKIP LOCKED so
// concurrent workers don't double-process), bumping attempts and pushing
// next_attempt_at out by lease so a crashed worker's job is re-claimable.
func (s *Store) ClaimDue(ctx context.Context, limit int, lease time.Duration) ([]SyncJob, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE sync_log SET attempts = attempts + 1, next_attempt_at = now() + $2::interval, updated_at = now()
		WHERE id IN (
			SELECT id FROM sync_log
			 WHERE status = 'pending' AND next_attempt_at <= now()
			 ORDER BY next_attempt_at
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		)
		RETURNING id, erp_event_id, kind, customer_ref, attempts, payload`,
		limit, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncJob
	for rows.Next() {
		var j SyncJob
		if err := rows.Scan(&j.ID, &j.ERPEventID, &j.Kind, &j.CustomerRef, &j.Attempts, &j.Payload); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkDone marks a job completed with its SeDoc result + correlation id.
func (s *Store) MarkDone(ctx context.Context, id uuid.UUID, result any, correlationID string) error {
	b, _ := json.Marshal(result)
	_, err := s.pool.Exec(ctx, `
		UPDATE sync_log SET status='done', sedoc_result=$2, correlation_id=$3, last_error='', updated_at=now()
		WHERE id=$1`, id, b, correlationID)
	return err
}

// MarkRetry keeps the job pending and schedules the next attempt after backoff.
func (s *Store) MarkRetry(ctx context.Context, id uuid.UUID, backoff time.Duration, lastErr, correlationID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sync_log SET status='pending', next_attempt_at=now()+$2::interval,
		       last_error=$3, correlation_id=$4, updated_at=now()
		WHERE id=$1`, id, backoff.String(), lastErr, correlationID)
	return err
}

// MarkFailed dead-letters the job (terminal).
func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, lastErr, correlationID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sync_log SET status='failed', last_error=$2, correlation_id=$3, updated_at=now()
		WHERE id=$1`, id, lastErr, correlationID)
	return err
}

// RetryFailed re-enqueues a dead-lettered job (Sync Dashboard "Retry").
func (s *Store) RetryFailed(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sync_log SET status='pending', next_attempt_at=now(), last_error='', updated_at=now()
		WHERE id=$1 AND status='failed'`, id)
	return err
}

// GetCustomer returns the mapping for a customer_ref, or ErrNotFound.
func (s *Store) GetCustomer(ctx context.Context, ref string) (*CustomerMapping, error) {
	var (
		m       CustomerMapping
		subJSON []byte
	)
	err := s.pool.QueryRow(ctx, `
		SELECT customer_ref, name, bucket_folder_id, main_folder_id, subfolder_ids
		FROM customer_map WHERE customer_ref=$1`, ref).
		Scan(&m.CustomerRef, &m.Name, &m.BucketFolderID, &m.MainFolderID, &subJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.SubfolderIDs = map[string]string{}
	_ = json.Unmarshal(subJSON, &m.SubfolderIDs)
	return &m, nil
}

// SaveCustomer upserts a customer mapping.
func (s *Store) SaveCustomer(ctx context.Context, m *CustomerMapping) error {
	sub, _ := json.Marshal(m.SubfolderIDs)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO customer_map (customer_ref, name, bucket_folder_id, main_folder_id, subfolder_ids)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (customer_ref) DO UPDATE
		  SET name=EXCLUDED.name, bucket_folder_id=EXCLUDED.bucket_folder_id,
		      main_folder_id=EXCLUDED.main_folder_id, subfolder_ids=EXCLUDED.subfolder_ids,
		      updated_at=now()`,
		m.CustomerRef, m.Name, m.BucketFolderID, m.MainFolderID, sub)
	return err
}

// CustomerByFolder reverse-maps a SeDoc folder id to the customer that owns it
// (its bucket, main, or any subfolder). The BFF uses this to authorize
// document-id routes: resolve the document's folder → customer → authz check.
// Returns ErrNotFound when no customer owns the folder.
func (s *Store) CustomerByFolder(ctx context.Context, folderID string) (string, error) {
	var ref string
	err := s.pool.QueryRow(ctx, `
		SELECT customer_ref FROM customer_map
		 WHERE main_folder_id = $1 OR bucket_folder_id = $1
		    OR EXISTS (SELECT 1 FROM jsonb_each_text(subfolder_ids) e WHERE e.value = $1)
		 LIMIT 1`, folderID).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return ref, err
}

// UpdateCustomerName updates the cached display name (after a rename).
func (s *Store) UpdateCustomerName(ctx context.Context, ref, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE customer_map SET name=$2, updated_at=now() WHERE customer_ref=$1`, ref, name)
	return err
}

// EnsureBucket returns the shared bucket folder id for (workspace, label),
// creating it via create() only when absent. Concurrent jobs converge: the
// INSERT is ON CONFLICT DO NOTHING and the final SELECT returns the winner, so
// at most one bucket folder exists per shard even under a thundering herd.
func (s *Store) EnsureBucket(ctx context.Context, workspaceID, label string, create func() (string, error)) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT folder_id FROM folder_buckets WHERE workspace_id=$1 AND bucket_label=$2`,
		workspaceID, label).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	folderID, cerr := create()
	if cerr != nil {
		return "", cerr
	}
	if _, ierr := s.pool.Exec(ctx, `
		INSERT INTO folder_buckets (workspace_id, bucket_label, folder_id)
		VALUES ($1,$2,$3) ON CONFLICT (workspace_id, bucket_label) DO NOTHING`,
		workspaceID, label, folderID); ierr != nil {
		return "", ierr
	}
	// Re-read so a concurrent winner's id is the one we return + persist.
	if serr := s.pool.QueryRow(ctx,
		`SELECT folder_id FROM folder_buckets WHERE workspace_id=$1 AND bucket_label=$2`,
		workspaceID, label).Scan(&id); serr != nil {
		return "", serr
	}
	return id, nil
}

// TrackIngestion records an /ingest item for the review UI.
func (s *Store) TrackIngestion(ctx context.Context, itemID, customerRef, status string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO ingestion_tracking (ingestion_item_id, customer_ref, status)
		VALUES ($1,$2,$3)
		ON CONFLICT (ingestion_item_id) DO UPDATE SET status=EXCLUDED.status, updated_at=now()`,
		itemID, customerRef, status)
	return err
}

// SyncCounts returns the live queue backlog (pending) and dead-letter size
// (failed) from sync_log, for the dashboard's operational metrics.
func (s *Store) SyncCounts(ctx context.Context) (backlog, dlq int64, err error) {
	rows, qerr := s.pool.Query(ctx, `SELECT status, count(*) FROM sync_log GROUP BY status`)
	if qerr != nil {
		return 0, 0, qerr
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int64
		if serr := rows.Scan(&status, &n); serr != nil {
			return 0, 0, serr
		}
		switch status {
		case "pending":
			backlog = n
		case "failed":
			dlq = n
		}
	}
	return backlog, dlq, rows.Err()
}

// SyncLogRow is a row for the dashboard.
type SyncLogRow struct {
	ID            uuid.UUID `json:"id"`
	ERPEventID    string    `json:"erp_event_id"`
	Kind          string    `json:"kind"`
	CustomerRef   string    `json:"customer_ref"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	CorrelationID string    `json:"correlation_id"`
	LastError     string    `json:"last_error"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ListSyncLog returns recent sync rows, optionally filtered by status.
func (s *Store) ListSyncLog(ctx context.Context, status string, limit int) ([]SyncLogRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, erp_event_id, kind, customer_ref, status, attempts, correlation_id, last_error, updated_at
	      FROM sync_log`
	args := []any{}
	if status != "" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncLogRow
	for rows.Next() {
		var r SyncLogRow
		if err := rows.Scan(&r.ID, &r.ERPEventID, &r.Kind, &r.CustomerRef, &r.Status,
			&r.Attempts, &r.CorrelationID, &r.LastError, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
