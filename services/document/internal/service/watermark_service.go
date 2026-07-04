package service

import (
	"context"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// Dynamic viewer watermark config + resolution (§5). Role-gating (owner/admin)
// happens at the admin handler; the per-document resolve path is gated by
// EnsureCanViewDocument in the viewer handler.

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (s *DocumentService) GetWatermarkConfig(ctx context.Context) (model.WatermarkConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return model.WatermarkConfig{}, err
	}
	var cfg model.WatermarkConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cfg, err = s.repos.Watermark.GetConfig(ctx, tx, tenantID)
		return err
	})
	return cfg, err
}

func (s *DocumentService) UpsertWatermarkConfig(ctx context.Context, cfg model.WatermarkConfig) (model.WatermarkConfig, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return model.WatermarkConfig{}, err
	}
	if err := validateWatermarkStyle(cfg.Template, cfg.Opacity, cfg.RotationDeg, cfg.FontSize, cfg.Color); err != nil {
		return model.WatermarkConfig{}, err
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Watermark.UpsertConfig(ctx, tx, tenantID, userID, cfg)
	})
	return cfg, err
}

func (s *DocumentService) ListWatermarkOverrides(ctx context.Context) ([]model.WatermarkOverride, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.WatermarkOverride
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Watermark.ListOverrides(ctx, tx, tenantID)
		return err
	})
	return out, err
}

var validWatermarkClasses = map[string]bool{
	model.ClassUnclassified: true, model.ClassInternal: true, model.ClassConfidential: true,
	model.ClassRestricted: true, model.WatermarkClassPHI: true,
}

func (s *DocumentService) UpsertWatermarkOverride(ctx context.Context, in model.WatermarkOverride) (model.WatermarkOverride, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return model.WatermarkOverride{}, err
	}
	if !validWatermarkClasses[in.Classification] {
		return model.WatermarkOverride{}, vdmserr.Validation("classification", "must be unclassified|internal|confidential|restricted|phi")
	}
	if in.Opacity != nil && (*in.Opacity < 0 || *in.Opacity > 100) {
		return model.WatermarkOverride{}, vdmserr.Validation("opacity", "must be 0-100")
	}
	var out model.WatermarkOverride
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Watermark.UpsertOverride(ctx, tx, tenantID, userID, in)
		return err
	})
	return out, err
}

func (s *DocumentService) DeleteWatermarkOverride(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, derr := s.repos.Watermark.DeleteOverride(ctx, tx, tenantID, id)
		if derr != nil {
			return derr
		}
		if !ok {
			return vdmserr.NotFound("override not found")
		}
		return nil
	})
}

// ResolveWatermarkForDoc loads the document plus the tenant config + overrides
// and returns the effective (pre-token-substitution) watermark style. The
// caller (viewer handler) has already run EnsureCanViewDocument. The returned
// document carries the classification the caller needs for download gating and
// the {classification} token.
func (s *DocumentService) ResolveWatermarkForDoc(ctx context.Context, docID uuid.UUID) (model.EffectiveWatermark, *model.Document, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return model.EffectiveWatermark{}, nil, err
	}
	var (
		cfg model.WatermarkConfig
		ovs []model.WatermarkOverride
		doc *model.Document
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		d, derr := s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
		if derr != nil {
			return derr
		}
		doc = d
		cfg, derr = s.repos.Watermark.GetConfig(ctx, tx, tenantID)
		if derr != nil {
			return derr
		}
		ovs, derr = s.repos.Watermark.ListOverrides(ctx, tx, tenantID)
		return derr
	})
	if err != nil {
		return model.EffectiveWatermark{}, nil, err
	}
	eff := model.ResolveWatermark(cfg, ovs, doc.SecurityClassification, doc.HasPHI)
	return eff, doc, nil
}

func validateWatermarkStyle(template string, opacity, rotation, fontSize int, color string) error {
	if template == "" {
		return vdmserr.Validation("template", "required")
	}
	if len(template) > 500 {
		return vdmserr.Validation("template", "too long (max 500)")
	}
	if opacity < 0 || opacity > 100 {
		return vdmserr.Validation("opacity", "must be 0-100")
	}
	if rotation < -180 || rotation > 180 {
		return vdmserr.Validation("rotation_deg", "must be -180..180")
	}
	if fontSize < 6 || fontSize > 200 {
		return vdmserr.Validation("font_size", "must be 6-200")
	}
	if !hexColorRe.MatchString(color) {
		return vdmserr.Validation("color", "must be #RRGGBB")
	}
	return nil
}
