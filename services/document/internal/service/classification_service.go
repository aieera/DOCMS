package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// Classification-based access control admin surface (§8): the tenant gate
// config, the classification→access rules, per-user clearance, and manual
// document classification. Role-gating (owner/admin) happens at the handler.

func (s *DocumentService) GetClassificationConfig(ctx context.Context) (model.ClassificationGateConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return model.ClassificationGateConfig{}, err
	}
	var cfg model.ClassificationGateConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cfg, err = s.repos.Classification.GetGateConfig(ctx, tx, tenantID)
		return err
	})
	return cfg, err
}

func (s *DocumentService) UpsertClassificationConfig(ctx context.Context, enabled, phiRequiresRestricted bool) (model.ClassificationGateConfig, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return model.ClassificationGateConfig{}, err
	}
	cfg := model.ClassificationGateConfig{Enabled: enabled, PHIRequiresRestricted: phiRequiresRestricted}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Classification.UpsertGateConfig(ctx, tx, tenantID, userID, cfg)
	})
	return cfg, err
}

func (s *DocumentService) ListClassificationRules(ctx context.Context) ([]model.ClassificationRule, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.ClassificationRule
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Classification.ListRules(ctx, tx, tenantID)
		return err
	})
	return out, err
}

var validRuleActions = map[string]bool{
	"*": true, "view": true, "view_unredacted": true, "download": true,
	"share": true, "edit": true, "delete": true,
}

func (s *DocumentService) CreateClassificationRule(ctx context.Context, in model.ClassificationRule) (model.ClassificationRule, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return model.ClassificationRule{}, err
	}
	// A rule's classification/clearance must be a concrete ladder level ("" is
	// not a valid rule bound — it means "unset", which the identity default
	// already handles).
	if in.MinClassification == "" || !model.ValidClassification(in.MinClassification) {
		return model.ClassificationRule{}, vdmserr.Validation("min_classification", "must be unclassified|internal|confidential|restricted")
	}
	if in.RequiredClearance == "" || !model.ValidClassification(in.RequiredClearance) {
		return model.ClassificationRule{}, vdmserr.Validation("required_clearance", "must be unclassified|internal|confidential|restricted")
	}
	if !validRuleActions[in.Action] {
		return model.ClassificationRule{}, vdmserr.Validation("action", "invalid action")
	}
	var out model.ClassificationRule
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Classification.CreateRule(ctx, tx, tenantID, userID, in)
		return err
	})
	return out, err
}

func (s *DocumentService) DeleteClassificationRule(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, derr := s.repos.Classification.DeleteRule(ctx, tx, tenantID, id)
		if derr != nil {
			return derr
		}
		if !ok {
			return vdmserr.NotFound("rule not found")
		}
		return nil
	})
}

func (s *DocumentService) SetUserClearance(ctx context.Context, userID uuid.UUID, clearance string) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if !model.ValidClassification(clearance) {
		return vdmserr.Validation("clearance", "must be ''|unclassified|internal|confidential|restricted")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, uerr := s.repos.Classification.SetUserClearance(ctx, tx, tenantID, userID, clearance)
		if uerr != nil {
			return uerr
		}
		if !ok {
			return vdmserr.NotFound("user not found")
		}
		return nil
	})
}

// SetDocumentClassification is the admin manual override. It marks the document
// with an authoritative 'manual' source that the scan consumer won't overwrite.
func (s *DocumentService) SetDocumentClassification(ctx context.Context, docID uuid.UUID, class string) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if class == "" || !model.ValidClassification(class) {
		return vdmserr.Validation("security_classification", "must be unclassified|internal|confidential|restricted")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, serr := s.repos.Classification.SetDocumentClassificationManual(ctx, tx, tenantID, docID, class)
		if serr != nil {
			return serr
		}
		if !ok {
			return vdmserr.NotFound("document not found")
		}
		return nil
	})
}
