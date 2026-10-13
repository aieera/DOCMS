//go:build integration && prodposture
// +build integration,prodposture

// L-11 (workflow part): the iPaaS "workflow completed" trigger feed read
// workflow_instances on the raw pool. workflow_instances is FORCE RLS, so
// under the prod NOBYPASSRLS role the feed was empty (or errored) for every
// Zapier / Make / n8n poll. This drives the real handler against a dms_app
// database.
package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/handler"
)

func TestProdPosture_IntegrationTriggerWorkflowsCompleted(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	// workflow_instances as in document 000001, minus the FKs to parents
	// this test does not need; RLS exactly as the schema of record.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE workflow_instances (
			tenant_id             UUID NOT NULL,
			id                    UUID NOT NULL DEFAULT gen_random_uuid(),
			definition_id         UUID NOT NULL,
			document_id           UUID,
			status                TEXT NOT NULL DEFAULT 'pending',
			current_step_id       TEXT,
			temporal_workflow_id  TEXT,
			started_by            UUID,
			started_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
			completed_at          TIMESTAMPTZ,
			PRIMARY KEY (tenant_id, id)
		);
		ALTER TABLE workflow_instances ENABLE ROW LEVEL SECURITY;
		ALTER TABLE workflow_instances FORCE  ROW LEVEL SECURITY;
		CREATE POLICY wf_inst_tenant_isolation ON workflow_instances
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY wf_inst_tenant_isolation_insert ON workflow_instances
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	seed := func(tenant uuid.UUID, status string) string {
		id := uuid.Must(uuid.NewV7())
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO workflow_instances (tenant_id, id, definition_id, status, started_at, completed_at)
				VALUES ($1, $2, $3, $4, now() - interval '10 minutes', CASE WHEN $4 = 'completed' THEN now() END)`,
				tenant, id, uuid.Must(uuid.NewV7()), status)
			return err
		}))
		return id.String()
	}
	doneA := seed(tenantA, "completed")
	seed(tenantA, "running")
	seed(tenantB, "completed")

	mux := http.NewServeMux()
	handler.NewIntegrationTriggersHandler(db.App).Register(mux)
	// APIKeyAuth stamps the key's tenant on the context in production.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/triggers/workflows/completed", nil)
	req = req.WithContext(auth.SetTenantID(req.Context(), tenantA))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var got []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1, "the caller's completed workflow, and only its own tenant's")
	require.Equal(t, doneA, got[0].ID)
}
