// Package repository is the Postgres data layer for the acknowledgement
// service. All methods take a pgx.Tx and must be invoked inside
// database.WithTenantTx so RLS enforces tenant isolation.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

// Repo is the full data surface — narrow wrapper, callers handle txs.
type Repo interface {
	// Campaigns.
	InsertCampaign(ctx context.Context, tx pgx.Tx, c *model.Campaign) error
	GetCampaign(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Campaign, error)
	ListCampaigns(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status model.Status) ([]model.Campaign, error)
	UpdateCampaignStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.Status, closedAt *time.Time) error

	// Assignments.
	BulkInsertAssignments(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, users []uuid.UUID) (int, error)
	GetAssignment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Assignment, error)
	ListAssignmentsByCampaign(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID) ([]model.Assignment, error)
	ListPendingForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.Assignment, error)
	MarkAcknowledged(ctx context.Context, tx pgx.Tx, a *model.Assignment) error

	// Events (hash chain).
	AppendEvent(ctx context.Context, tx pgx.Tx, e *model.Event) error
	ChainHeadForCampaign(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID) (*model.ChainHead, error)

	// Report rollups.
	ReportCounts(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, now time.Time) (total, acked, overdue, escalated int, err error)

	// Attestation-key persistence.
	GetSigningKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (kekID string, wrapped []byte, err error)
	InsertSigningKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kekID string, wrapped []byte) error
}

type repo struct{}

// New returns a stateless repo bundle.
func New() Repo { return &repo{} }

// ---- campaigns ------------------------------------------------------------

func (r *repo) InsertCampaign(ctx context.Context, tx pgx.Tx, c *model.Campaign) error {
	policy, _ := json.Marshal(c.RecipientPolicy)
	if len(policy) == 0 {
		policy = []byte("{}")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO acknowledgement_campaigns (
			tenant_id, id, document_id, version_id, title, body_md,
			due_at, created_by_user_id, status, recipient_policy
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		c.TenantID, c.ID, c.DocumentID, c.VersionID, c.Title, c.BodyMD,
		c.DueAt, c.CreatedByUserID, string(c.Status), policy)
	return mapPgError(err)
}

func (r *repo) GetCampaign(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Campaign, error) {
	row := tx.QueryRow(ctx, selectCampaignSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanCampaignRow(row)
}

func (r *repo) ListCampaigns(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status model.Status) ([]model.Campaign, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = tx.Query(ctx, selectCampaignSQL+` WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 500`, tenantID)
	} else {
		rows, err = tx.Query(ctx, selectCampaignSQL+` WHERE tenant_id = $1 AND status = $2 ORDER BY created_at DESC LIMIT 500`, tenantID, string(status))
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Campaign
	for rows.Next() {
		c, err := scanCampaignRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *repo) UpdateCampaignStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.Status, closedAt *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE acknowledgement_campaigns
		   SET status = $3, closed_at = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, string(s), closedAt)
	return mapPgError(err)
}

// ---- assignments ----------------------------------------------------------

// BulkInsertAssignments inserts one row per user. Uses ON CONFLICT
// DO NOTHING so re-resolving a recipient policy is idempotent.
func (r *repo) BulkInsertAssignments(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, users []uuid.UUID) (int, error) {
	if len(users) == 0 {
		return 0, nil
	}
	rows := make([][]any, 0, len(users))
	for _, u := range users {
		rows = append(rows, []any{tenantID, campaignID, u})
	}
	// CopyFrom is fastest but doesn't support ON CONFLICT. For a
	// campaign, recipient counts are small (≤ tens of thousands) so
	// we issue one multi-row INSERT via pgx.Batch instead.
	batch := &pgx.Batch{}
	for _, row := range rows {
		batch.Queue(`
			INSERT INTO acknowledgement_assignments (tenant_id, campaign_id, assignee_user_id)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, campaign_id, assignee_user_id) DO NOTHING`,
			row...)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	inserted := 0
	for range rows {
		tag, err := br.Exec()
		if err != nil {
			return inserted, mapPgError(err)
		}
		inserted += int(tag.RowsAffected())
	}
	return inserted, nil
}

func (r *repo) GetAssignment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Assignment, error) {
	row := tx.QueryRow(ctx, selectAssignmentSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanAssignmentRow(row)
}

func (r *repo) ListAssignmentsByCampaign(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID) ([]model.Assignment, error) {
	rows, err := tx.Query(ctx, selectAssignmentSQL+`
		WHERE tenant_id = $1 AND campaign_id = $2
		ORDER BY assigned_at ASC`, tenantID, campaignID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Assignment
	for rows.Next() {
		a, err := scanAssignmentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *repo) ListPendingForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.Assignment, error) {
	rows, err := tx.Query(ctx, selectAssignmentSQL+`
		WHERE tenant_id = $1
		  AND assignee_user_id = $2
		  AND acknowledged_at IS NULL
		ORDER BY assigned_at ASC`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Assignment
	for rows.Next() {
		a, err := scanAssignmentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *repo) MarkAcknowledged(ctx context.Context, tx pgx.Tx, a *model.Assignment) error {
	tag, err := tx.Exec(ctx, `
		UPDATE acknowledgement_assignments
		   SET acknowledged_at = $3,
		       ip_address = NULLIF($4, '')::inet,
		       user_agent = $5,
		       comment = $6,
		       attestation_hash = $7
		 WHERE tenant_id = $1 AND id = $2
		   AND acknowledged_at IS NULL`,
		a.TenantID, a.ID, a.AcknowledgedAt, a.IPAddress, a.UserAgent, a.Comment, a.AttestationHash)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		// Either not found or already acknowledged — treat as idempotent.
		return vdmserr.Conflict("assignment already acknowledged or not found")
	}
	return nil
}

// ---- events (hash chain) --------------------------------------------------

func (r *repo) AppendEvent(ctx context.Context, tx pgx.Tx, e *model.Event) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO acknowledgement_events (
			tenant_id, id, campaign_id, assignment_id, actor_user_id,
			event_type, payload, prev_hash, self_hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.TenantID, e.ID, e.CampaignID, e.AssignmentID, e.ActorUserID,
		e.EventType, e.Payload, e.PrevHash, e.SelfHash)
	return mapPgError(err)
}

func (r *repo) ChainHeadForCampaign(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID) (*model.ChainHead, error) {
	row := tx.QueryRow(ctx, `
		SELECT self_hash FROM acknowledgement_events
		 WHERE tenant_id = $1 AND campaign_id = $2
		 ORDER BY created_at DESC LIMIT 1`, tenantID, campaignID)
	var h []byte
	if err := row.Scan(&h); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &model.ChainHead{}, nil
		}
		return nil, mapPgError(err)
	}
	return &model.ChainHead{SelfHash: h}, nil
}

// ---- report ---------------------------------------------------------------

func (r *repo) ReportCounts(ctx context.Context, tx pgx.Tx, tenantID, campaignID uuid.UUID, now time.Time) (int, int, int, int, error) {
	row := tx.QueryRow(ctx, `
		SELECT
			count(*) AS total,
			count(*) FILTER (WHERE a.acknowledged_at IS NOT NULL) AS acked,
			count(*) FILTER (WHERE a.acknowledged_at IS NULL AND c.due_at <  $3) AS overdue,
			count(*) FILTER (WHERE a.escalated_at    IS NOT NULL) AS escalated
		  FROM acknowledgement_assignments a
		  JOIN acknowledgement_campaigns  c
		    ON c.tenant_id = a.tenant_id AND c.id = a.campaign_id
		 WHERE a.tenant_id = $1 AND a.campaign_id = $2`,
		tenantID, campaignID, now)
	var total, acked, overdue, escalated int
	if err := row.Scan(&total, &acked, &overdue, &escalated); err != nil {
		return 0, 0, 0, 0, mapPgError(err)
	}
	return total, acked, overdue, escalated, nil
}

// ---- scan helpers ---------------------------------------------------------

const selectCampaignSQL = `
	SELECT tenant_id, id, document_id, version_id,
	       title, COALESCE(body_md, ''), due_at, created_by_user_id,
	       status, recipient_policy, closed_at, created_at, updated_at
	  FROM acknowledgement_campaigns`

const selectAssignmentSQL = `
	SELECT tenant_id, id, campaign_id, assignee_user_id,
	       assigned_at, reminded_count, reminded_at,
	       acknowledged_at, COALESCE(host(ip_address), ''),
	       COALESCE(user_agent, ''), COALESCE(comment, ''),
	       COALESCE(attestation_hash, ''::bytea), escalated_at
	  FROM acknowledgement_assignments`

type scanner interface{ Scan(...any) error }

func scanCampaignRow(s scanner) (*model.Campaign, error) {
	var (
		c      model.Campaign
		status string
		policy []byte
		ver    *uuid.UUID
		closed *time.Time
	)
	if err := s.Scan(&c.TenantID, &c.ID, &c.DocumentID, &ver,
		&c.Title, &c.BodyMD, &c.DueAt, &c.CreatedByUserID,
		&status, &policy, &closed, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, mapPgError(err)
	}
	c.Status = model.Status(status)
	c.VersionID = ver
	c.ClosedAt = closed
	if len(policy) > 0 {
		_ = json.Unmarshal(policy, &c.RecipientPolicy)
	}
	return &c, nil
}

func scanAssignmentRow(s scanner) (*model.Assignment, error) {
	var (
		a            model.Assignment
		remindedAt   *time.Time
		ackedAt      *time.Time
		escalatedAt  *time.Time
	)
	if err := s.Scan(&a.TenantID, &a.ID, &a.CampaignID, &a.AssigneeUserID,
		&a.AssignedAt, &a.RemindedCount, &remindedAt,
		&ackedAt, &a.IPAddress, &a.UserAgent, &a.Comment,
		&a.AttestationHash, &escalatedAt); err != nil {
		return nil, mapPgError(err)
	}
	a.RemindedAt = remindedAt
	a.AcknowledgedAt = ackedAt
	a.EscalatedAt = escalatedAt
	return &a, nil
}

// ---- signing key ----------------------------------------------------------

func (r *repo) GetSigningKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (string, []byte, error) {
	row := tx.QueryRow(ctx, `
		SELECT kek_id, wrapped_key FROM acknowledgement_signing_keys WHERE tenant_id = $1`, tenantID)
	var (
		kekID   string
		wrapped []byte
	)
	if err := row.Scan(&kekID, &wrapped); err != nil {
		return "", nil, mapPgError(err)
	}
	return kekID, wrapped, nil
}

func (r *repo) InsertSigningKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kekID string, wrapped []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO acknowledgement_signing_keys (tenant_id, kek_id, wrapped_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id) DO NOTHING`, tenantID, kekID, wrapped)
	return mapPgError(err)
}

// ---- error mapping --------------------------------------------------------

func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return vdmserr.Wrap(vdmserr.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return vdmserr.Wrap(vdmserr.ErrAlreadyExists, err)
		case "23503":
			return vdmserr.Wrap(vdmserr.Conflict("foreign key violation"), err)
		}
	}
	return fmt.Errorf("acknowledgement db: %w", err)
}
