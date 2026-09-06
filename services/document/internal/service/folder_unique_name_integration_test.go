//go:build integration
// +build integration

// QA SD-05: two folders could share a name in the same parent — grid,
// tree, and breadcrumb became ambiguous. Migration 000103 adds a live-rows
// unique index per (workspace, parent, lower(name)); the service turns the
// violation into a friendly conflict.
package service_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

func TestCreateFolder_RefusesDuplicateNameInSameParent(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	root := mustCreateFolder(t, h, wsID, "Root")

	_, err := h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID, ParentFolderID: &root.ID, Name: "QA Folder",
	})
	require.NoError(t, err)

	_, err = h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID, ParentFolderID: &root.ID, Name: "QA Folder",
	})
	require.Error(t, err, "second folder with the same name in the same parent must be refused")
	require.True(t, errors.Is(err, vdmserr.ErrAlreadyExists),
		"want already-exists conflict, got %v", err)
	require.Contains(t, strings.ToLower(err.Error()), "already exists",
		"message should say a folder with this name already exists")

	// Case-insensitive: "qa folder" collides with "QA Folder".
	_, err = h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID, ParentFolderID: &root.ID, Name: "qa folder",
	})
	require.Error(t, err)

	// A different parent is fine.
	sub, err := h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID, ParentFolderID: &root.ID, Name: "Sub",
	})
	require.NoError(t, err)
	_, err = h.svc.CreateFolder(h.ctx, &service.CreateFolderInput{
		WorkspaceID: wsID, ParentFolderID: &sub.ID, Name: "QA Folder",
	})
	require.NoError(t, err)
}
