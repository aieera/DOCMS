//go:build integration
// +build integration

// H-10: UpdateDefinition / DeleteDefinition skipped both the visibility
// gate (checkDefinitionAccess) and the manage gate (canManageDefinition),
// so any tenant member could rewrite or delete any workflow template —
// including private templates they cannot even read.
package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/workflow/internal/model"
)

var reviewSteps = []model.Step{{Name: "review", Type: "approval"}}

func createDef(t *testing.T, s *Service, tenant, creator uuid.UUID, visibility string) *model.WorkflowDefinition {
	t.Helper()
	d, err := s.CreateDefinition(as(tenant, creator, "member"), tenant.String(),
		"tpl-"+uuid.NewString(), "original", creator.String(), visibility, reviewSteps)
	require.NoError(t, err)
	return d
}

// readDef reads the row as a tenant admin (bypasses the visibility filter).
func readDef(t *testing.T, s *Service, tenant uuid.UUID, id string) *model.WorkflowDefinition {
	t.Helper()
	d, err := s.repo.GetDefinition(as(tenant, uuid.Nil, "admin"), tenant.String(), id)
	require.NoError(t, err)
	require.NotNil(t, d)
	return d
}

func activeDefinitionIDs(t *testing.T, tenant uuid.UUID) map[string]bool {
	t.Helper()
	rows, err := testPool.Query(t.Context(),
		`SELECT id::text FROM workflow_definitions WHERE tenant_id = $1 AND is_active`, tenant)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out[id] = true
	}
	return out
}

func TestUpdateDefinition_MemberForbidden(t *testing.T) {
	s, _ := newTestService(t)
	tenant, creator, member := newIDs()
	def := createDef(t, s, tenant, creator, model.VisibilityShared)

	// A member who can SEE the shared template but neither owns nor created
	// it, and is not an admin, must not rewrite it.
	_, err := s.UpdateDefinition(as(tenant, member, "member"), tenant.String(), def.ID, "hijacked", "x", nil)
	require.Truef(t, errors.Is(err, ErrForbidden), "member update: err = %v, want ErrForbidden", err)
	got := readDef(t, s, tenant, def.ID)
	require.Equal(t, def.Name, got.Name, "a refused update must not change the row")
	require.Len(t, got.Steps, 1)

	// The creator and a tenant admin may.
	_, err = s.UpdateDefinition(as(tenant, creator, "member"), tenant.String(), def.ID, "by-creator", "x", reviewSteps)
	require.NoError(t, err)
	_, err = s.UpdateDefinition(as(tenant, member, "admin"), tenant.String(), def.ID, "by-admin", "x", reviewSteps)
	require.NoError(t, err)
	require.Equal(t, "by-admin", readDef(t, s, tenant, def.ID).Name)
}

func TestUpdateDefinition_PrivateNotFound(t *testing.T) {
	s, _ := newTestService(t)
	tenant, owner, member := newIDs()
	def := createDef(t, s, tenant, owner, model.VisibilityPrivate)

	// A private template the caller cannot read answers exactly like a
	// missing one (existence-leak guard, mirrors GetDefinition).
	_, err := s.UpdateDefinition(as(tenant, member, "member"), tenant.String(), def.ID, "hijacked", "x", nil)
	require.Truef(t, errors.Is(err, ErrNotFound), "update of unreadable private template: err = %v, want ErrNotFound", err)
	require.Equal(t, def.Name, readDef(t, s, tenant, def.ID).Name)
}

func TestDeleteDefinition_PrivateNotFound(t *testing.T) {
	s, _ := newTestService(t)
	tenant, owner, member := newIDs()
	def := createDef(t, s, tenant, owner, model.VisibilityPrivate)

	err := s.DeleteDefinition(as(tenant, member, "member"), tenant.String(), def.ID)
	require.Truef(t, errors.Is(err, ErrNotFound), "delete of unreadable private template: err = %v, want ErrNotFound", err)
	require.True(t, activeDefinitionIDs(t, tenant)[def.ID], "a refused delete must leave the template active")

	// The owner may delete it.
	require.NoError(t, s.DeleteDefinition(as(tenant, owner, "member"), tenant.String(), def.ID))
	require.False(t, activeDefinitionIDs(t, tenant)[def.ID])
}

func TestDeleteDefinition_MemberForbidden(t *testing.T) {
	s, _ := newTestService(t)
	tenant, creator, member := newIDs()
	shared := createDef(t, s, tenant, creator, model.VisibilityShared)
	private := createDef(t, s, tenant, creator, model.VisibilityPrivate)
	// A grant lets the member READ (and launch) the private template; it does
	// not make them its manager.
	_, err := s.AddGrant(as(tenant, creator, "member"), tenant.String(), private.ID, "user", member.String())
	require.NoError(t, err)

	for _, d := range []*model.WorkflowDefinition{shared, private} {
		err := s.DeleteDefinition(as(tenant, member, "member"), tenant.String(), d.ID)
		require.Truef(t, errors.Is(err, ErrForbidden), "%s delete by member: err = %v, want ErrForbidden", d.Visibility, err)
		require.True(t, activeDefinitionIDs(t, tenant)[d.ID])
	}
}

func TestUpdateDefinition_MissingNotFound(t *testing.T) {
	s, _ := newTestService(t)
	tenant, _, member := newIDs()
	_, err := s.UpdateDefinition(as(tenant, member, "admin"), tenant.String(), uuid.NewString(), "x", "x", nil)
	require.Truef(t, errors.Is(err, ErrNotFound), "err = %v, want ErrNotFound", err)
	err = s.DeleteDefinition(as(tenant, member, "admin"), tenant.String(), uuid.NewString())
	require.Truef(t, errors.Is(err, ErrNotFound), "err = %v, want ErrNotFound", err)
}
