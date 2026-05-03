// Repository for ADR 0053 — three concerns kept in one file because
// they share lookup paths (route suggestions reference rules + history,
// the analytics endpoint joins all three).
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// ---- types ----------------------------------------------------------------

type RouteSuggestion struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	DocumentID           uuid.UUID
	VersionID            uuid.UUID
	SuggestedFolderID    uuid.UUID
	SuggestedWorkspaceID *uuid.UUID
	FolderPath           string
	MatchSource          string // 'rule'|'history'|'similarity'
	MatchDetail          json.RawMessage
	Confidence           float32
	Status               string // 'pending'|'accepted'|'dismissed'
	AcceptedBy           *uuid.UUID
	AcceptedAt           *time.Time
	CreatedAt            time.Time
}

type RoutingRule struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	Name               string
	Description        string
	CategoryKey        string
	TargetFolderID     uuid.UUID
	TargetWorkspaceID  *uuid.UUID
	Priority           int32
	Enabled            bool
	CreatedBy          uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type RoutingRuleInput struct {
	Name               string
	Description        string
	CategoryKey        string
	TargetFolderID     uuid.UUID
	TargetWorkspaceID  *uuid.UUID
	Priority           int32
	Enabled            bool
}

type RoutingRulePatch struct {
	Name        *string
	Description *string
	Priority    *int32
	Enabled     *bool
}

type SmartRoutingConfig struct {
	TenantID           uuid.UUID
	Enabled            bool
	AutoMoveThreshold  float32
	SuggestThreshold   float32
	MaxSuggestions     int32
	LearnFromHistory   bool
	UseSimilarity      bool
}

type SmartRoutingConfigPatch struct {
	Enabled           *bool
	AutoMoveThreshold *float32
	SuggestThreshold  *float32
	MaxSuggestions    *int32
	LearnFromHistory  *bool
	UseSimilarity     *bool
}

type FilingHistoryRow struct {
	TenantID         uuid.UUID
	DocumentID       uuid.UUID
	CategoryKey      string
	FiledFolderID    uuid.UUID
	FiledWorkspaceID *uuid.UUID
	WasSuggestion    bool
	SuggestionRank   *int32
	FiledBy          uuid.UUID
}

// FilingAnalytics is a small bundle the admin page renders.
type FilingAnalytics struct {
	TopCategories         []CategoryFrequency
	TopFolderByCategory   []CategoryFolderFrequency
	SuggestionAcceptance  SuggestionAcceptanceStats
	TotalFilings          int64
}

type CategoryFrequency struct {
	CategoryKey string
	Count       int64
}

type CategoryFolderFrequency struct {
	CategoryKey string
	FolderID    uuid.UUID
	Count       int64
}

type SuggestionAcceptanceStats struct {
	TotalSuggested int64
	TotalAccepted  int64
	TotalDismissed int64
	AcceptanceRate float64
}

// ---- interface ------------------------------------------------------------

type RoutingRepository interface {
	// Suggestions
	ListSuggestionsForDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]RouteSuggestion, error)
	GetSuggestion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*RouteSuggestion, error)
	MarkSuggestionStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus string) (*RouteSuggestion, error)

	// Rules CRUD
	ListRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]RoutingRule, error)
	GetRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*RoutingRule, error)
	InsertRule(ctx context.Context, tx pgx.Tx, tenantID, createdBy uuid.UUID, in RoutingRuleInput) (*RoutingRule, error)
	UpdateRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, p RoutingRulePatch) (*RoutingRule, error)
	DeleteRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error

	// Config
	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*SmartRoutingConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p SmartRoutingConfigPatch) (*SmartRoutingConfig, error)

	// Filing history (the learning loop)
	InsertFilingHistory(ctx context.Context, tx pgx.Tx, row FilingHistoryRow) error
	Analytics(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*FilingAnalytics, error)
}

type routingRepo struct{}

func NewRoutingRepo() RoutingRepository { return &routingRepo{} }

// ---- suggestions ---------------------------------------------------------

const selectSuggestionSQL = `
SELECT id, tenant_id, document_id, version_id, suggested_folder_id,
       suggested_workspace_id, folder_path, match_source, match_detail,
       confidence, status, accepted_by, accepted_at, created_at
  FROM route_suggestions
`

func (r *routingRepo) ListSuggestionsForDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]RouteSuggestion, error) {
	rows, err := tx.Query(ctx, selectSuggestionSQL+`
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY confidence DESC, created_at DESC
		LIMIT 50`,
		tenantID, docID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]RouteSuggestion, 0, 8)
	for rows.Next() {
		s, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, mapPgError(rows.Err())
}

func (r *routingRepo) GetSuggestion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*RouteSuggestion, error) {
	row := tx.QueryRow(ctx, selectSuggestionSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanSuggestion(row)
}

func (r *routingRepo) MarkSuggestionStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus string) (*RouteSuggestion, error) {
	if newStatus != "accepted" && newStatus != "dismissed" {
		return nil, vdmserr.Validation("status", "must be accepted or dismissed")
	}
	row := tx.QueryRow(ctx, `
		UPDATE route_suggestions
		   SET status = $4, accepted_by = $3, accepted_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'
		 RETURNING id, tenant_id, document_id, version_id, suggested_folder_id,
		           suggested_workspace_id, folder_path, match_source, match_detail,
		           confidence, status, accepted_by, accepted_at, created_at`,
		tenantID, id, reviewer, newStatus,
	)
	s, err := scanSuggestion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vdmserr.ErrNotFound) {
			return nil, vdmserr.Conflict("suggestion is not pending")
		}
		return nil, err
	}
	return s, nil
}

// ---- rules CRUD ----------------------------------------------------------

const selectRuleSQL = `
SELECT id, tenant_id, name, COALESCE(description, ''), category_key,
       target_folder_id, target_workspace_id, priority, enabled,
       created_by, created_at, updated_at
  FROM routing_rules
`

func (r *routingRepo) ListRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]RoutingRule, error) {
	rows, err := tx.Query(ctx, selectRuleSQL+`
		WHERE tenant_id = $1
		ORDER BY priority DESC, name ASC
		LIMIT 500`,
		tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]RoutingRule, 0, 16)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rule)
	}
	return out, mapPgError(rows.Err())
}

func (r *routingRepo) GetRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*RoutingRule, error) {
	row := tx.QueryRow(ctx, selectRuleSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanRule(row)
}

func (r *routingRepo) InsertRule(ctx context.Context, tx pgx.Tx, tenantID, createdBy uuid.UUID, in RoutingRuleInput) (*RoutingRule, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO routing_rules
		    (tenant_id, name, description, category_key, target_folder_id,
		     target_workspace_id, priority, enabled, created_by)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9)
		RETURNING id, tenant_id, name, COALESCE(description, ''), category_key,
		          target_folder_id, target_workspace_id, priority, enabled,
		          created_by, created_at, updated_at`,
		tenantID, in.Name, in.Description, in.CategoryKey,
		in.TargetFolderID, in.TargetWorkspaceID, in.Priority, in.Enabled,
		createdBy,
	)
	return scanRule(row)
}

func (r *routingRepo) UpdateRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, p RoutingRulePatch) (*RoutingRule, error) {
	cur, err := r.GetRule(ctx, tx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		cur.Name = *p.Name
	}
	if p.Description != nil {
		cur.Description = *p.Description
	}
	if p.Priority != nil {
		cur.Priority = *p.Priority
	}
	if p.Enabled != nil {
		cur.Enabled = *p.Enabled
	}
	row := tx.QueryRow(ctx, `
		UPDATE routing_rules
		   SET name = $3, description = NULLIF($4, ''),
		       priority = $5, enabled = $6
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, tenant_id, name, COALESCE(description, ''), category_key,
		           target_folder_id, target_workspace_id, priority, enabled,
		           created_by, created_at, updated_at`,
		tenantID, id, cur.Name, cur.Description, cur.Priority, cur.Enabled,
	)
	return scanRule(row)
}

func (r *routingRepo) DeleteRule(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM routing_rules WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ---- config --------------------------------------------------------------

const selectConfigSQL = `
SELECT tenant_id, enabled, auto_move_threshold, suggest_threshold,
       max_suggestions, learn_from_history, use_similarity
  FROM smart_routing_config
`

func (r *routingRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*SmartRoutingConfig, error) {
	row := tx.QueryRow(ctx, selectConfigSQL+` WHERE tenant_id = $1`, tenantID)
	return scanConfig(row)
}

func (r *routingRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p SmartRoutingConfigPatch) (*SmartRoutingConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := SmartRoutingConfig{
		TenantID:          tenantID,
		Enabled:           true,
		AutoMoveThreshold: 0.98,
		SuggestThreshold:  0.50,
		MaxSuggestions:    5,
		LearnFromHistory:  true,
		UseSimilarity:     true,
	}
	if cur != nil {
		merged = *cur
	}
	if p.Enabled != nil {
		merged.Enabled = *p.Enabled
	}
	if p.AutoMoveThreshold != nil {
		merged.AutoMoveThreshold = *p.AutoMoveThreshold
	}
	if p.SuggestThreshold != nil {
		merged.SuggestThreshold = *p.SuggestThreshold
	}
	if p.MaxSuggestions != nil {
		merged.MaxSuggestions = *p.MaxSuggestions
	}
	if p.LearnFromHistory != nil {
		merged.LearnFromHistory = *p.LearnFromHistory
	}
	if p.UseSimilarity != nil {
		merged.UseSimilarity = *p.UseSimilarity
	}
	if merged.AutoMoveThreshold < merged.SuggestThreshold {
		return nil, vdmserr.Validation("auto_move_threshold",
			"must be >= suggest_threshold")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO smart_routing_config
		    (tenant_id, enabled, auto_move_threshold, suggest_threshold,
		     max_suggestions, learn_from_history, use_similarity)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled              = EXCLUDED.enabled,
		       auto_move_threshold  = EXCLUDED.auto_move_threshold,
		       suggest_threshold    = EXCLUDED.suggest_threshold,
		       max_suggestions      = EXCLUDED.max_suggestions,
		       learn_from_history   = EXCLUDED.learn_from_history,
		       use_similarity       = EXCLUDED.use_similarity,
		       updated_at           = now()`,
		tenantID, merged.Enabled, merged.AutoMoveThreshold, merged.SuggestThreshold,
		merged.MaxSuggestions, merged.LearnFromHistory, merged.UseSimilarity,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

// ---- filing history ------------------------------------------------------

func (r *routingRepo) InsertFilingHistory(ctx context.Context, tx pgx.Tx, row FilingHistoryRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO filing_history
		    (tenant_id, document_id, category_key, filed_folder_id,
		     filed_workspace_id, was_suggestion, suggestion_rank, filed_by)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8)`,
		row.TenantID, row.DocumentID, row.CategoryKey, row.FiledFolderID,
		row.FiledWorkspaceID, row.WasSuggestion, row.SuggestionRank, row.FiledBy,
	)
	return mapPgError(err)
}

func (r *routingRepo) Analytics(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*FilingAnalytics, error) {
	out := &FilingAnalytics{}

	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM filing_history WHERE tenant_id = $1`, tenantID,
	).Scan(&out.TotalFilings); err != nil {
		return nil, mapPgError(err)
	}

	rows, err := tx.Query(ctx, `
		SELECT category_key, COUNT(*) AS cnt
		  FROM filing_history
		 WHERE tenant_id = $1 AND category_key IS NOT NULL
		 GROUP BY category_key
		 ORDER BY cnt DESC
		 LIMIT 10`, tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var c CategoryFrequency
		if err := rows.Scan(&c.CategoryKey, &c.Count); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.TopCategories = append(out.TopCategories, c)
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT category_key, filed_folder_id, COUNT(*) AS cnt
		  FROM filing_history
		 WHERE tenant_id = $1 AND category_key IS NOT NULL
		 GROUP BY category_key, filed_folder_id
		 ORDER BY cnt DESC
		 LIMIT 25`, tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var cf CategoryFolderFrequency
		if err := rows.Scan(&cf.CategoryKey, &cf.FolderID, &cf.Count); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.TopFolderByCategory = append(out.TopFolderByCategory, cf)
	}
	rows.Close()

	if err := tx.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM route_suggestions WHERE tenant_id = $1),
		  (SELECT COUNT(*) FROM route_suggestions WHERE tenant_id = $1 AND status = 'accepted'),
		  (SELECT COUNT(*) FROM route_suggestions WHERE tenant_id = $1 AND status = 'dismissed')`,
		tenantID,
	).Scan(
		&out.SuggestionAcceptance.TotalSuggested,
		&out.SuggestionAcceptance.TotalAccepted,
		&out.SuggestionAcceptance.TotalDismissed,
	); err != nil {
		return nil, mapPgError(err)
	}
	if out.SuggestionAcceptance.TotalSuggested > 0 {
		out.SuggestionAcceptance.AcceptanceRate =
			float64(out.SuggestionAcceptance.TotalAccepted) /
				float64(out.SuggestionAcceptance.TotalSuggested)
	}
	return out, nil
}

// ---- scanners ------------------------------------------------------------

func scanSuggestion(s rowScanner) (*RouteSuggestion, error) {
	var (
		out          RouteSuggestion
		matchDetail  []byte
		workspace    *uuid.UUID
		acceptedBy   *uuid.UUID
		acceptedAt   *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.DocumentID, &out.VersionID,
		&out.SuggestedFolderID, &workspace, &out.FolderPath,
		&out.MatchSource, &matchDetail, &out.Confidence, &out.Status,
		&acceptedBy, &acceptedAt, &out.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.SuggestedWorkspaceID = workspace
	out.MatchDetail = json.RawMessage(matchDetail)
	out.AcceptedBy = acceptedBy
	out.AcceptedAt = acceptedAt
	return &out, nil
}

func scanRule(s rowScanner) (*RoutingRule, error) {
	var (
		out       RoutingRule
		workspace *uuid.UUID
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.Name, &out.Description, &out.CategoryKey,
		&out.TargetFolderID, &workspace, &out.Priority, &out.Enabled,
		&out.CreatedBy, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.TargetWorkspaceID = workspace
	return &out, nil
}

func scanConfig(s rowScanner) (*SmartRoutingConfig, error) {
	var c SmartRoutingConfig
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.AutoMoveThreshold, &c.SuggestThreshold,
		&c.MaxSuggestions, &c.LearnFromHistory, &c.UseSimilarity,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}
