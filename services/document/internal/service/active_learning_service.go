// Active learning service surface (ADR 0060). Read-+-promote API on
// the document service; the actual training/evaluation tasks live on
// the intelligence side.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func (s *DocumentService) ListModelVersions(ctx context.Context, opts repository.ListVersionsOpts) ([]repository.ModelVersion, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	var (
		rows  []repository.ModelVersion
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.ActiveLearning.ListVersions(ctx, tx, tenantID, opts)
		return lerr
	})
	return rows, total, err
}

func (s *DocumentService) GetModelVersion(ctx context.Context, id uuid.UUID) (*repository.ModelVersion, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.ModelVersion
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		v, gErr := s.repos.ActiveLearning.GetVersion(ctx, tx, tenantID, id)
		if gErr != nil {
			return gErr
		}
		out = v
		return nil
	})
	return out, err
}

// PromoteModel marks the given version production and atomically
// retires the previous production model. Emits dms.model.promoted.v1
// so intelligence workers evict their LRU cache for this tenant.
func (s *DocumentService) PromoteModel(ctx context.Context, id uuid.UUID) (*repository.ModelVersion, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var promoted *repository.ModelVersion
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if pErr := s.repos.ActiveLearning.Promote(ctx, tx, tenantID, id, userID); pErr != nil {
			return pErr
		}
		v, gErr := s.repos.ActiveLearning.GetVersion(ctx, tx, tenantID, id)
		if gErr != nil {
			return gErr
		}
		promoted = v
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.model.promoted.v1", "model_version", id,
			modelPromotedPayload{
				VersionID:     id.String(),
				TenantID:      tenantID.String(),
				ModelType:     v.ModelType,
				VersionTag:    v.VersionTag,
				PromotedBy:    userID.String(),
				PromotedAt:    time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return promoted, err
}

func (s *DocumentService) RetireModel(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.ActiveLearning.Retire(ctx, tx, tenantID, id)
	})
}

// TriggerRetrain emits dms.model.retrain_requested.v1; the
// intelligence consumer subscribes and dispatches the Celery task.
func (s *DocumentService) TriggerRetrain(ctx context.Context, modelType string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if modelType == "" {
		modelType = "classification"
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.model.retrain_requested.v1", "model_version", uuid.New(),
			modelRetrainRequestedPayload{
				TenantID:    tenantID.String(),
				ModelType:   modelType,
				RequestedBy: userID.String(),
				RequestedAt: time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

func (s *DocumentService) TrainingExampleStats(ctx context.Context) (*repository.TrainingExampleSummary, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.TrainingExampleSummary
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		st, sErr := s.repos.ActiveLearning.ExampleStats(ctx, tx, tenantID)
		if sErr != nil {
			return sErr
		}
		out = st
		return nil
	})
	return out, err
}

func (s *DocumentService) DeleteTrainingExample(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.ActiveLearning.DeleteExample(ctx, tx, tenantID, id)
	})
}

func (s *DocumentService) GetActiveLearningConfig(ctx context.Context) (*repository.ActiveLearningConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.ActiveLearningConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.ActiveLearning.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertActiveLearningConfig(ctx context.Context, p repository.ActiveLearningConfigPatch) (*repository.ActiveLearningConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateActiveLearningPatch(p); err != nil {
		return nil, err
	}
	var out *repository.ActiveLearningConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.ActiveLearning.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

func validateActiveLearningPatch(p repository.ActiveLearningConfigPatch) error {
	if p.MinExamplesForRetrain != nil && *p.MinExamplesForRetrain < 10 {
		return vdmserr.Validation("min_examples_for_retrain", "must be >= 10")
	}
	if p.RetrainIncrement != nil && *p.RetrainIncrement < 1 {
		return vdmserr.Validation("retrain_increment", "must be >= 1")
	}
	if p.MinAccuracyImprovement != nil && *p.MinAccuracyImprovement < 0 {
		return vdmserr.Validation("min_accuracy_improvement", "must be >= 0")
	}
	for name, v := range map[string]*float32{
		"train_validation_split": p.TrainValidationSplit,
		"train_test_split":       p.TrainTestSplit,
	} {
		if v != nil && (*v < 0 || *v > 0.5) {
			return vdmserr.Validation(name, "must be in [0.0, 0.5]")
		}
	}
	return nil
}

type modelPromotedPayload struct {
	VersionID  string `json:"version_id"`
	TenantID   string `json:"tenant_id"`
	ModelType  string `json:"model_type"`
	VersionTag string `json:"version_tag"`
	PromotedBy string `json:"promoted_by"`
	PromotedAt string `json:"promoted_at"`
}

type modelRetrainRequestedPayload struct {
	TenantID    string `json:"tenant_id"`
	ModelType   string `json:"model_type"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
}
