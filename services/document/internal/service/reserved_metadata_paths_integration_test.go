//go:build integration

// D-25 (review round 2): two more ways a user could get a reserved `_sedoc.`
// key onto a document — the system marker the orphan GC trusts.
//
//   - Create-from-template: provisionDoc inserted the template's metadata
//     straight through repos.Documents.Create, skipping the reserved-key
//     check. Template create/update now refuse such keys, and provisioning
//     refuses them too (covers templates stored before this rule).
//   - Copy: the copy inherited the source's metadata verbatim, so copying a
//     version-less ingest produced a new version-less document carrying the
//     marker — a user-minted GC exemption. The marker records which server
//     path created THIS row and a copy is a user action, so Copy always
//     strips `_sedoc.*` (whether or not it also copies a version).
package service_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

const reservedKeyTemplate = `{
  "nodes": [
    {
      "name": "{{project}}-root",
      "metadata": { "_sedoc.source": "m365" },
      "docs": [ { "title": "{{project}} charter" } ]
    }
  ]
}`

func TestCreateFromTemplate_RejectsReservedKeys(t *testing.T) {
	h := newProvisionHarness(t)
	wsID := h.seedWorkspace(t)

	// Template create and update refuse a reserved key.
	_, err := h.svc.CreateTemplate(h.ctx, service.TemplateInput{
		Name: "spoof", Definition: json.RawMessage(reservedKeyTemplate),
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	require.ErrorContains(t, err, "_sedoc.")
	tplID := h.createTemplate(t)
	_, err = h.svc.UpdateTemplate(h.ctx, tplID, service.TemplateInput{
		Name: "spoof", Definition: json.RawMessage(reservedKeyTemplate),
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))

	// A template stored before the rule still cannot provision the key.
	_, err = h.pool.Exec(h.ctx, `UPDATE workspace_templates SET definition = $3 WHERE tenant_id = $1 AND id = $2`,
		h.tenant, tplID, reservedKeyTemplate)
	require.NoError(t, err)
	_, err = h.svc.ProvisionFromTemplate(h.ctx, service.ProvisionInput{
		TemplateID: tplID, WorkspaceID: wsID, Variables: map[string]string{"project": "apollo"},
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
	var marked int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND custom_metadata ? '_sedoc.source'`, h.tenant).Scan(&marked))
	require.Zero(t, marked)
	require.Zero(t, h.folderCount(t, wsID), "the refused provision scaffolds nothing")
}

func TestCopy_DoesNotInheritSystemMetadata(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Copies")
	src, err := h.svc.CreateDocument(h.ctx, &service.CreateDocumentInput{
		WorkspaceID: wsID, FolderID: f.ID, Title: "ingested", UpdatedBy: h.user,
		CustomMetadata: map[string]any{"matter": "x"},
		SystemMetadata: map[string]any{service.SystemSourceKey: service.SystemSourceM365},
	})
	require.NoError(t, err)

	cp, err := h.svc.CopyDocument(h.ctx, &service.CopyDocumentInput{
		DocumentID: src.ID, TargetFolderID: f.ID, CopiedBy: h.user,
	})
	require.NoError(t, err)
	require.Equal(t, 1, countRows(t, h, `SELECT count(*) FROM documents
		WHERE tenant_id = $1 AND id = $2 AND NOT (custom_metadata ? '_sedoc.source')
		  AND custom_metadata->>'matter' = 'x'`, h.tenant, cp.ID),
		"the copy keeps user metadata but not the system marker")
	require.Equal(t, 1, countRows(t, h, `SELECT count(*) FROM documents
		WHERE tenant_id = $1 AND id = $2 AND custom_metadata->>'_sedoc.source' = 'm365'`, h.tenant, src.ID),
		"the source keeps its marker")
}
