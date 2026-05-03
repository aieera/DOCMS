// NER service surface (ADR 0061). Read entities for a doc; record a
// label correction. The actual extraction runs on the intelligence side.
package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// allowedEntityTypes is the ADR 0061 taxonomy plus the two legacy
// regex types that already shipped (credit_card, percent). Validators
// enforce this on writes (corrections + manual adds) but reads
// pass through anything in the table — old data may have other types.
var allowedEntityTypes = map[string]bool{
	// PII
	"name": true, "email": true, "phone": true, "national_id": true,
	"address": true, "dob": true, "patient_id": true, "credit_card": true,
	// Financial
	"amount": true, "currency": true, "account_number": true, "tax_id": true,
	// Legal
	"party_name": true, "effective_date": true, "jurisdiction": true,
	"governing_law": true,
	// Medical
	"icd_code": true, "cpt_code": true,
	// Misc passthrough — dropped from new writes but tolerated on reads.
	"percent": true,
}

var allowedCorrectionActions = map[string]bool{
	"relabel": true, "add": true, "delete": true, "confirm": true,
}

func (s *DocumentService) ListEntities(
	ctx context.Context,
	documentID uuid.UUID,
	opts repository.ListEntitiesOpts,
) ([]repository.Entity, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, mustCallerUserID(ctx), documentID, "view"); perr != nil {
		return nil, 0, perr
	}
	var (
		rows  []repository.Entity
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.NER.ListByDocument(ctx, tx, tenantID, documentID, opts)
		return lerr
	})
	return rows, total, err
}

// CorrectEntityInput is the validated shape coming out of the handler.
type CorrectEntityInput struct {
	OriginalEntityID *uuid.UUID
	OriginalType     string
	CorrectedType    string
	EntityValue      string
	StartOffset      int32
	EndOffset        int32
	Action           string // 'relabel'|'add'|'delete'|'confirm'
	Note             string
}

// CorrectEntity records a manual correction in entity_corrections AND
// applies the action to document_entities so the read path reflects
// the user's intent immediately. The active-learning pipeline picks
// up entity_corrections on its next retrain trigger.
//
// Atomicity: correction + entity mutation + outbox event in one tx.
func (s *DocumentService) CorrectEntity(
	ctx context.Context,
	documentID uuid.UUID,
	in CorrectEntityInput,
) (*repository.EntityCorrection, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateCorrectEntity(in); err != nil {
		return nil, err
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, userID, documentID, "edit"); perr != nil {
		return nil, perr
	}

	var out *repository.EntityCorrection
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Resolve the version the correction targets. For relabel/delete
		// we look it up off the original entity; for add we use the
		// document's current version.
		versionID, vErr := s.repos.NER.CurrentVersionID(ctx, tx, tenantID, documentID)
		if vErr != nil {
			return vErr
		}
		// For relabel/delete, the original entity must exist and belong
		// to this document.
		if in.Action == "relabel" || in.Action == "delete" {
			if in.OriginalEntityID == nil {
				return vdmserr.Validation("original_entity_id", "required for relabel/delete")
			}
			ent, gErr := s.repos.NER.GetEntity(ctx, tx, tenantID, *in.OriginalEntityID)
			if gErr != nil {
				return vdmserr.Wrap(vdmserr.ErrNotFound, gErr)
			}
			if ent.DocumentID != documentID {
				return vdmserr.Validation("original_entity_id", "belongs to a different document")
			}
			versionID = ent.VersionID
			if in.OriginalType == "" {
				in.OriginalType = ent.EntityType
			}
		}
		// Persist the correction ledger row.
		correction, cErr := s.repos.NER.InsertCorrection(ctx, tx, tenantID, userID, repository.EntityCorrectionInput{
			DocumentID:       documentID,
			VersionID:        versionID,
			OriginalEntityID: in.OriginalEntityID,
			OriginalType:     in.OriginalType,
			CorrectedType:    in.CorrectedType,
			EntityValue:      in.EntityValue,
			StartOffset:      in.StartOffset,
			EndOffset:        in.EndOffset,
			Action:           in.Action,
			Note:             in.Note,
		})
		if cErr != nil {
			return cErr
		}
		// Apply the action to document_entities.
		switch in.Action {
		case "relabel":
			if uErr := s.repos.NER.UpdateEntityType(ctx, tx, tenantID, *in.OriginalEntityID, in.CorrectedType); uErr != nil {
				return uErr
			}
		case "delete":
			if dErr := s.repos.NER.DeleteEntity(ctx, tx, tenantID, *in.OriginalEntityID); dErr != nil {
				return dErr
			}
		case "add":
			if _, iErr := s.repos.NER.InsertEntity(ctx, tx, tenantID, repository.Entity{
				DocumentID:  documentID,
				VersionID:   versionID,
				EntityType:  in.CorrectedType,
				EntityValue: in.EntityValue,
				StartOffset: in.StartOffset,
				EndOffset:   in.EndOffset,
				Confidence:  1.0,
				IsPII:       isPIIType(in.CorrectedType),
				Source:      "manual",
			}); iErr != nil {
				return iErr
			}
		case "confirm":
			// Ledger-only: the user is endorsing the existing label as
			// training signal, no document_entities mutation needed.
		}
		out = correction
		// Outbox: training collector subscribes to this and feeds
		// the corrected (text, label) pair into the per-tenant NER
		// fine-tune queue.
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.entity.corrected.v1", "entity_correction", correction.ID,
			entityCorrectedPayload{
				CorrectionID:   correction.ID.String(),
				TenantID:       tenantID.String(),
				DocumentID:     documentID.String(),
				VersionID:      versionID.String(),
				OriginalType:   in.OriginalType,
				CorrectedType:  in.CorrectedType,
				EntityValue:    in.EntityValue,
				Action:         in.Action,
				CorrectedBy:    userID.String(),
				CorrectedAt:    time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

func (s *DocumentService) ListEntityCorrections(ctx context.Context, documentID uuid.UUID) ([]repository.EntityCorrection, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if _, perr := s.requireDocPermission(ctx, tenantID, mustCallerUserID(ctx), documentID, "view"); perr != nil {
		return nil, perr
	}
	var out []repository.EntityCorrection
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.NER.ListCorrections(ctx, tx, tenantID, documentID)
		return lerr
	})
	return out, err
}

// validateCorrectEntity is exported via the service tests' file-internal
// alias so we can unit-test it without a DB.
func validateCorrectEntity(in CorrectEntityInput) error {
	if !allowedCorrectionActions[in.Action] {
		return vdmserr.Validation("action", "must be one of relabel|add|delete|confirm")
	}
	switch in.Action {
	case "add", "relabel", "confirm":
		if in.CorrectedType == "" {
			return vdmserr.Validation("corrected_type", "required")
		}
		if !allowedEntityTypes[in.CorrectedType] {
			return vdmserr.Validation("corrected_type", "unknown entity type")
		}
	}
	if in.Action == "add" {
		if strings.TrimSpace(in.EntityValue) == "" {
			return vdmserr.Validation("entity_value", "required for add")
		}
		if in.EndOffset <= in.StartOffset {
			return vdmserr.Validation("end_offset", "must be greater than start_offset")
		}
	}
	if len(in.Note) > 1000 {
		return vdmserr.Validation("note", "max 1000 chars")
	}
	return nil
}

func isPIIType(t string) bool {
	switch t {
	case "name", "email", "phone", "national_id", "address", "dob",
		"patient_id", "credit_card", "account_number", "tax_id":
		return true
	default:
		return false
	}
}

// mustCallerUserID is a small convenience used in the read paths above
// where we only need the user ID for the permission check.
func mustCallerUserID(ctx context.Context) uuid.UUID {
	_, userID, _ := mustCaller(ctx)
	return userID
}

type entityCorrectedPayload struct {
	CorrectionID  string `json:"correction_id"`
	TenantID      string `json:"tenant_id"`
	DocumentID    string `json:"document_id"`
	VersionID     string `json:"version_id"`
	OriginalType  string `json:"original_type"`
	CorrectedType string `json:"corrected_type"`
	EntityValue   string `json:"entity_value"`
	Action        string `json:"action"`
	CorrectedBy   string `json:"corrected_by"`
	CorrectedAt   string `json:"corrected_at"`
}
