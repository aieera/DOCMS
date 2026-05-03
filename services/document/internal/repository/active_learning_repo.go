// Active learning repository (ADR 0060) — model_versions +
// training_examples + per-tenant config in one file.
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

// ---- types --------------------------------------------------------------

type ModelVersion struct {
	ID                    uuid.UUID
	TenantID              uuid.UUID
	ModelType             string
	VersionTag            string
	BaseModel             string
	S3ArtifactPath        string
	TrainingExamplesCount int32
	TrainingMetrics       json.RawMessage
	EvalMetrics           json.RawMessage
	Status                string // training|evaluating|candidate|production|retired|failed
	ErrorMessage          string
	PromotedAt            *time.Time
	PromotedBy            *uuid.UUID
	RetiredAt             *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type TrainingExampleSummary struct {
	TotalCount      int64
	UnusedCount     int64
	PerLabel        []LabelCount
	PerSplit        map[string]int64
}

type LabelCount struct {
	Label string
	Count int64
}

type ActiveLearningConfig struct {
	TenantID                uuid.UUID
	Enabled                 bool
	MinExamplesForRetrain   int32
	RetrainIncrement        int32
	AutoPromoteIfBetter     bool
	MinAccuracyImprovement  float32
	TrainValidationSplit    float32
	TrainTestSplit          float32
	GpuQueue                string
}

type ActiveLearningConfigPatch struct {
	Enabled                 *bool
	MinExamplesForRetrain   *int32
	RetrainIncrement        *int32
	AutoPromoteIfBetter     *bool
	MinAccuracyImprovement  *float32
	TrainValidationSplit    *float32
	TrainTestSplit          *float32
	GpuQueue                *string
}

type ListVersionsOpts struct {
	ModelType string
	Status    string
	Limit     int32
	Offset    int32
}

// ---- interface ----------------------------------------------------------

type ActiveLearningRepository interface {
	// Model versions
	ListVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListVersionsOpts) ([]ModelVersion, int64, error)
	GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*ModelVersion, error)
	GetProduction(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, modelType string) (*ModelVersion, error)
	Promote(ctx context.Context, tx pgx.Tx, tenantID, id, userID uuid.UUID) error
	Retire(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error

	// Training examples
	ExampleStats(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*TrainingExampleSummary, error)
	DeleteExample(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error

	// Config
	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ActiveLearningConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p ActiveLearningConfigPatch) (*ActiveLearningConfig, error)
}

type activeLearningRepo struct{}

func NewActiveLearningRepo() ActiveLearningRepository { return &activeLearningRepo{} }

// ---- model versions -----------------------------------------------------

const selectModelVersionSQL = `
SELECT id, tenant_id, model_type, version_tag, base_model,
       s3_artifact_path, training_examples_count, training_metrics, eval_metrics,
       status, COALESCE(error_message, ''), promoted_at, promoted_by,
       retired_at, created_at, updated_at
  FROM model_versions
`

func (r *activeLearningRepo) ListVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListVersionsOpts) ([]ModelVersion, int64, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mt := opts.ModelType
	st := opts.Status

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM model_versions
		 WHERE tenant_id = $1
		   AND ($2 = '' OR model_type = $2)
		   AND ($3 = '' OR status = $3)`,
		tenantID, mt, st,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}
	rows, err := tx.Query(ctx, selectModelVersionSQL+`
		WHERE tenant_id = $1
		  AND ($2 = '' OR model_type = $2)
		  AND ($3 = '' OR status = $3)
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`,
		tenantID, mt, st, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]ModelVersion, 0, opts.Limit)
	for rows.Next() {
		v, err := scanModelVersion(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	return out, total, mapPgError(rows.Err())
}

func (r *activeLearningRepo) GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*ModelVersion, error) {
	row := tx.QueryRow(ctx, selectModelVersionSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanModelVersion(row)
}

func (r *activeLearningRepo) GetProduction(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, modelType string) (*ModelVersion, error) {
	row := tx.QueryRow(ctx, selectModelVersionSQL+`
		WHERE tenant_id = $1 AND model_type = $2 AND status = 'production'`,
		tenantID, modelType,
	)
	return scanModelVersion(row)
}

// Promote: atomic swap. Retires the previous production model (if any)
// and marks the supplied id production. The partial unique index on
// status='production' makes any non-atomic attempt fail loudly.
func (r *activeLearningRepo) Promote(ctx context.Context, tx pgx.Tx, tenantID, id, userID uuid.UUID) error {
	target, err := r.GetVersion(ctx, tx, tenantID, id)
	if err != nil {
		return err
	}
	if target.Status != "candidate" && target.Status != "evaluating" {
		return vdmserr.Conflict("only candidate or evaluating models can be promoted")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE model_versions
		   SET status = 'retired', retired_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND model_type = $2 AND status = 'production'`,
		tenantID, target.ModelType,
	); err != nil {
		return mapPgError(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE model_versions
		   SET status = 'production',
		       promoted_at = now(),
		       promoted_by = $3,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, userID,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *activeLearningRepo) Retire(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE model_versions
		   SET status = 'retired', retired_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND status NOT IN ('retired', 'failed')`,
		tenantID, id,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.Conflict("model is already retired/failed or not found")
	}
	return nil
}

// ---- training examples --------------------------------------------------

func (r *activeLearningRepo) ExampleStats(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*TrainingExampleSummary, error) {
	out := &TrainingExampleSummary{
		PerSplit: map[string]int64{"train": 0, "validation": 0, "test": 0},
	}
	if err := tx.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE used_in_version IS NULL)
		FROM training_examples
		WHERE tenant_id = $1`, tenantID,
	).Scan(&out.TotalCount, &out.UnusedCount); err != nil {
		return nil, mapPgError(err)
	}
	rows, err := tx.Query(ctx, `
		SELECT split, COUNT(*) FROM training_examples
		 WHERE tenant_id = $1 GROUP BY split`,
		tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var split string
		var count int64
		if err := rows.Scan(&split, &count); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.PerSplit[split] = count
	}
	rows.Close()
	rows, err = tx.Query(ctx, `
		SELECT label, COUNT(*) AS cnt FROM training_examples
		 WHERE tenant_id = $1
		 GROUP BY label
		 ORDER BY cnt DESC LIMIT 25`,
		tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var lc LabelCount
		if err := rows.Scan(&lc.Label, &lc.Count); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.PerLabel = append(out.PerLabel, lc)
	}
	rows.Close()
	return out, nil
}

func (r *activeLearningRepo) DeleteExample(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		DELETE FROM training_examples WHERE tenant_id = $1 AND id = $2`,
		tenantID, id,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ---- config -------------------------------------------------------------

const selectActiveLearningConfigSQL = `
SELECT tenant_id, enabled, min_examples_for_retrain, retrain_increment,
       auto_promote_if_better, min_accuracy_improvement,
       train_validation_split, train_test_split, gpu_queue
  FROM active_learning_config
`

func (r *activeLearningRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ActiveLearningConfig, error) {
	row := tx.QueryRow(ctx, selectActiveLearningConfigSQL+` WHERE tenant_id = $1`, tenantID)
	return scanActiveLearningConfig(row)
}

func (r *activeLearningRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p ActiveLearningConfigPatch) (*ActiveLearningConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := ActiveLearningConfig{
		TenantID:                tenantID,
		Enabled:                 false,
		MinExamplesForRetrain:   50,
		RetrainIncrement:        25,
		AutoPromoteIfBetter:     false,
		MinAccuracyImprovement:  0.02,
		TrainValidationSplit:    0.10,
		TrainTestSplit:          0.10,
		GpuQueue:                "intelligence-gpu",
	}
	if cur != nil {
		merged = *cur
	}
	if p.Enabled != nil {
		merged.Enabled = *p.Enabled
	}
	if p.MinExamplesForRetrain != nil {
		merged.MinExamplesForRetrain = *p.MinExamplesForRetrain
	}
	if p.RetrainIncrement != nil {
		merged.RetrainIncrement = *p.RetrainIncrement
	}
	if p.AutoPromoteIfBetter != nil {
		merged.AutoPromoteIfBetter = *p.AutoPromoteIfBetter
	}
	if p.MinAccuracyImprovement != nil {
		merged.MinAccuracyImprovement = *p.MinAccuracyImprovement
	}
	if p.TrainValidationSplit != nil {
		merged.TrainValidationSplit = *p.TrainValidationSplit
	}
	if p.TrainTestSplit != nil {
		merged.TrainTestSplit = *p.TrainTestSplit
	}
	if p.GpuQueue != nil {
		merged.GpuQueue = *p.GpuQueue
	}
	if merged.TrainValidationSplit+merged.TrainTestSplit > 0.5 {
		return nil, vdmserr.Validation("splits", "train_validation + train_test must not exceed 0.5")
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO active_learning_config
		    (tenant_id, enabled, min_examples_for_retrain, retrain_increment,
		     auto_promote_if_better, min_accuracy_improvement,
		     train_validation_split, train_test_split, gpu_queue)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled                 = EXCLUDED.enabled,
		       min_examples_for_retrain = EXCLUDED.min_examples_for_retrain,
		       retrain_increment        = EXCLUDED.retrain_increment,
		       auto_promote_if_better   = EXCLUDED.auto_promote_if_better,
		       min_accuracy_improvement = EXCLUDED.min_accuracy_improvement,
		       train_validation_split   = EXCLUDED.train_validation_split,
		       train_test_split         = EXCLUDED.train_test_split,
		       gpu_queue                = EXCLUDED.gpu_queue,
		       updated_at               = now()`,
		tenantID, merged.Enabled, merged.MinExamplesForRetrain, merged.RetrainIncrement,
		merged.AutoPromoteIfBetter, merged.MinAccuracyImprovement,
		merged.TrainValidationSplit, merged.TrainTestSplit, merged.GpuQueue,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

// ---- scanners -----------------------------------------------------------

func scanModelVersion(s rowScanner) (*ModelVersion, error) {
	var (
		out             ModelVersion
		trainingMetrics []byte
		evalMetrics     []byte
		promotedBy      *uuid.UUID
		promotedAt      *time.Time
		retiredAt       *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.ModelType, &out.VersionTag, &out.BaseModel,
		&out.S3ArtifactPath, &out.TrainingExamplesCount,
		&trainingMetrics, &evalMetrics,
		&out.Status, &out.ErrorMessage,
		&promotedAt, &promotedBy, &retiredAt,
		&out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.TrainingMetrics = json.RawMessage(trainingMetrics)
	out.EvalMetrics = json.RawMessage(evalMetrics)
	out.PromotedBy = promotedBy
	out.PromotedAt = promotedAt
	out.RetiredAt = retiredAt
	return &out, nil
}

func scanActiveLearningConfig(s rowScanner) (*ActiveLearningConfig, error) {
	var c ActiveLearningConfig
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.MinExamplesForRetrain, &c.RetrainIncrement,
		&c.AutoPromoteIfBetter, &c.MinAccuracyImprovement,
		&c.TrainValidationSplit, &c.TrainTestSplit, &c.GpuQueue,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}
