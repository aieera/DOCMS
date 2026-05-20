// Service-layer smart folder methods — ADR 0100.
package service

import (
	"context"
	"errors"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// ListSmartFolders proxies to the repository. Workspace membership is
// resolved by the handler (it knows the caller's identity); the service
// just forwards the IDs.
func (s *Service) ListSmartFolders(
	ctx context.Context,
	tenantID, userID string,
	workspaceIDs []string,
) ([]*model.SavedSearch, error) {
	list, err := s.repo.ListSmartFolders(ctx, tenantID, userID, workspaceIDs)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []*model.SavedSearch{}
	}
	return list, nil
}

// PromoteSmartFolder validates visibility + workspace pairing and
// flips the row.
func (s *Service) PromoteSmartFolder(
	ctx context.Context,
	tenantID, userID, id, visibility, icon string,
	workspaceID *string,
) (*model.SavedSearch, error) {
	switch visibility {
	case "private", "workspace", "public":
	default:
		return nil, errors.New("invalid tree_visibility (private|workspace|public)")
	}
	if visibility == "workspace" && (workspaceID == nil || *workspaceID == "") {
		return nil, errors.New("workspace_id required for workspace visibility")
	}
	return s.repo.PromoteSmartFolder(ctx, tenantID, userID, id, visibility, icon, workspaceID)
}

func (s *Service) DemoteSmartFolder(ctx context.Context, tenantID, userID, id string) error {
	return s.repo.DemoteSmartFolder(ctx, tenantID, userID, id)
}
