// NER service surface (ADR 0078). Read entities for a doc; record a
// label correction. The actual extraction runs on the intelligence side.
package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// allowedEntityTypes is the ADR 0078 taxonomy plus the two legacy
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

// ---- NER config admin -----------------------------------------------------

func (s *DocumentService) GetNERConfig(ctx context.Context) (*repository.NERConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.NERConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.NER.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertNERConfig(ctx context.Context, p repository.NERConfigPatch) (*repository.NERConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateNERConfigPatch(p); err != nil {
		return nil, err
	}
	var out *repository.NERConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.NER.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

// SetLLMAPIKey encrypts the plaintext key with the per-deployment KEK
// and persists base64(nonce||ct) on the tenant's ner_config row. Audit:
// emits dms.ner_config.api_key_set.v1 (no payload of the plaintext).
func (s *DocumentService) SetLLMAPIKey(ctx context.Context, plain string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := validateAPIKey(plain); err != nil {
		return err
	}
	encoded, err := s.encryptTenantSecret(plain)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if sErr := s.repos.NER.SetEncryptedAPIKey(ctx, tx, tenantID, encoded, now); sErr != nil {
			return sErr
		}
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.ner_config.api_key_set.v1", "ner_config", tenantID,
			map[string]any{
				"tenant_id": tenantID.String(),
				"set_by":    userID.String(),
				"set_at":    now.Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// ClearLLMAPIKey unsets the per-tenant key. The LLM tier silently
// no-ops on the next call until a new key is set.
func (s *DocumentService) ClearLLMAPIKey(ctx context.Context) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if cErr := s.repos.NER.ClearAPIKey(ctx, tx, tenantID); cErr != nil {
			return cErr
		}
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.ner_config.api_key_cleared.v1", "ner_config", tenantID,
			map[string]any{
				"tenant_id":  tenantID.String(),
				"cleared_by": userID.String(),
				"cleared_at": time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// encryptTenantSecret wraps a small secret (≤ a few KB) with the
// service's local KEK (AES-256-GCM). Returns base64(nonce || ct).
// Same wire format the auth service uses for MFA secrets, and the
// Python intelligence worker decrypts using the same key + format
// (services/intelligence/app/secrets.py).
func (s *DocumentService) encryptTenantSecret(plain string) (string, error) {
	if len(s.localKEK) != crypto.DEKSize {
		return "", vdmserr.Internal("local KEK not configured (set VAULTDMS_LOCAL_KEK)")
	}
	ct, nonce, err := crypto.EncryptData([]byte(plain), s.localKEK)
	if err != nil {
		return "", fmt.Errorf("encrypt tenant secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func validateAPIKey(plain string) error {
	p := strings.TrimSpace(plain)
	if p != plain {
		return vdmserr.Validation("api_key", "must not have leading/trailing whitespace")
	}
	if len(p) < 16 {
		return vdmserr.Validation("api_key", "looks too short to be a real key")
	}
	if len(p) > 512 {
		return vdmserr.Validation("api_key", "max 512 chars")
	}
	return nil
}

func validateNERConfigPatch(p repository.NERConfigPatch) error {
	if p.BatchSize != nil && (*p.BatchSize < 1 || *p.BatchSize > 20) {
		return vdmserr.Validation("llm_batch_size", "must be in [1, 20]")
	}
	if p.MinConfidence != nil && (*p.MinConfidence < 0 || *p.MinConfidence > 1) {
		return vdmserr.Validation("llm_min_confidence", "must be in [0.0, 1.0]")
	}
	if p.Model != nil && strings.TrimSpace(*p.Model) == "" {
		return vdmserr.Validation("llm_model", "must not be empty")
	}
	if p.EntityTypes != nil {
		for _, t := range *p.EntityTypes {
			if !allowedEntityTypes[t] {
				return vdmserr.Validation("llm_entity_types", "unknown entity type: "+t)
			}
		}
	}
	return nil
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
