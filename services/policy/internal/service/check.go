package service

import (
	"context"
	"sync"

	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
	"github.com/vaultdms/vaultdms/services/policy/internal/opa"
)

// CheckInput is the public input type. Handlers map the proto into this.
type CheckInput struct {
	TenantID     uuid.UUID
	SubjectType  string
	SubjectID    string
	Action       string
	ResourceType string
	ResourceID   string
	Context      map[string]string
}

// Check runs a single permission decision. Returns (allowed, reason).
func (s *Service) Check(ctx context.Context, in CheckInput) (model.CheckResult, error) {
	if err := validateCheck(in); err != nil {
		return model.CheckResult{}, err
	}
	resourceID, err := uuid.Parse(in.ResourceID)
	if err != nil {
		return model.CheckResult{}, vdmserr.Validation("resource_id", "not a uuid")
	}

	perms, err := s.loadResourcePermissions(ctx, in.TenantID,
		model.ResourceType(in.ResourceType), resourceID,
		in.Context["folder_id"], in.Context["workspace_id"])
	if err != nil {
		return model.CheckResult{}, err
	}

	var userGroups []string
	var workspaces []opa.WorkspaceDoc
	if in.SubjectType == string(model.PrincUser) {
		if sid, err := uuid.Parse(in.SubjectID); err == nil {
			userGroups, _ = s.loadUserGroups(ctx, in.TenantID, sid)
			workspaces, _ = s.loadUserWorkspaces(ctx, in.TenantID, sid)
		}
	}

	result, dur, err := s.engine.Eval(ctx, opa.EvalInput{
		Input: model.CheckInput{
			SubjectType:  in.SubjectType,
			SubjectID:    in.SubjectID,
			Action:       in.Action,
			ResourceType: in.ResourceType,
			ResourceID:   in.ResourceID,
			Context:      in.Context,
		},
		Permissions: perms,
		UserGroups:  userGroups,
		Workspaces:  workspaces,
	})
	if err != nil {
		return model.CheckResult{}, err
	}
	if dur.Milliseconds() > 20 {
		s.log.Warn().Dur("elapsed", dur).Int("perms", len(perms)).Msg("slow rego eval")
	}
	return result, nil
}

// BatchCheck runs up to 50 checks in parallel (bounded goroutine pool)
// and returns results in the same order as inputs.
func (s *Service) BatchCheck(ctx context.Context, inputs []CheckInput) ([]model.CheckResult, error) {
	const maxBatch = 50
	if len(inputs) > maxBatch {
		return nil, vdmserr.Validation("checks", "batch size exceeds 50")
	}
	out := make([]model.CheckResult, len(inputs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // cap parallelism to bound DB/Redis load
	for i := range inputs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.Check(ctx, inputs[i])
			if err != nil {
				out[i] = model.CheckResult{Allowed: false, Reason: "internal error"}
				return
			}
			out[i] = res
		}(i)
	}
	wg.Wait()
	return out, nil
}

// ---- validation -----------------------------------------------------------

var (
	validSubjectTypes  = map[string]struct{}{"user": {}, "group": {}}
	validResourceTypes = map[string]struct{}{"document": {}, "folder": {}, "workspace": {}}
	// view_unredacted (ADR 0062) — gated download of the source
	// version after a candidate-review redaction has produced a
	// redacted current version. Hierarchy rank 15, between view (10)
	// and share (20); see services/policy/internal/opa/policy.rego.
	validActions       = map[string]struct{}{"view": {}, "view_unredacted": {}, "share": {}, "edit": {}, "delete": {}, "admin": {}}
)

func validateCheck(in CheckInput) error {
	if in.TenantID == uuid.Nil {
		return vdmserr.Validation("tenant_id", "required")
	}
	if _, ok := validSubjectTypes[in.SubjectType]; !ok {
		return vdmserr.Validation("subject_type", "must be user or group")
	}
	if in.SubjectID == "" {
		return vdmserr.Validation("subject_id", "required")
	}
	if _, ok := validResourceTypes[in.ResourceType]; !ok {
		return vdmserr.Validation("resource_type", "must be document, folder, or workspace")
	}
	if in.ResourceID == "" {
		return vdmserr.Validation("resource_id", "required")
	}
	if _, ok := validActions[in.Action]; !ok {
		return vdmserr.Validation("action", "unsupported action")
	}
	return nil
}
