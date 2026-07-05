//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1.a (issue #70) — the saved-search ALERT path under the prod
// NOBYPASSRLS posture. The Temporal activities behind
// SavedSearchAlertWorkflow read/write the FORCE-RLS saved_searches and
// saved_search_subscribers tables; before the fix, LoadSavedSearchAlert
// and UpdateSavedSearchAlertCursor ran on the raw pool, so in prod the
// load returned no rows and alerts silently never fired.
//
// This test drives the REAL activity chain end-to-end against a dms_app
// NOBYPASSRLS database: load → match (search API stubbed via httptest —
// OpenSearch relevance is not the RLS surface under test; the stub also
// proves the owner-scoped identity headers) → cursor write → alert
// emission into the transactional outbox. "An ingested matching doc
// fires an alert" = the outbox carries dms.notify.saved_search_match.v1
// for the subscriber, tenant-scoped.
package activities_test

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

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

func TestProdPosture_SavedSearchAlertFires(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")

	// The alert path spans two schemas: the search service's tables
	// (its migration chain is standalone-clean) and the shared outbox
	// (schema-of-record DDL from document 000001 + the actor columns
	// the publisher reads).
	require.NoError(t, database.RunServiceMigrations(db.SuperDSN, "../../../search/migrations", "search"))
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE outbox (
			id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id       UUID NOT NULL,
			event_type      TEXT NOT NULL,
			aggregate_type  TEXT NOT NULL,
			aggregate_id    UUID NOT NULL,
			payload         JSONB NOT NULL,
			published       BOOLEAN NOT NULL DEFAULT false,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			published_at    TIMESTAMPTZ,
			actor_id        UUID,
			actor_name      TEXT,
			ip_address      INET,
			user_agent      TEXT
		);
		ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
		ALTER TABLE outbox FORCE  ROW LEVEL SECURITY;
		CREATE POLICY outbox_tenant_isolation ON outbox
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
			WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	owner := uuid.Must(uuid.NewV7())
	subscriber := uuid.Must(uuid.NewV7())
	ssID := uuid.Must(uuid.NewV7())
	matchedDoc := uuid.Must(uuid.NewV7()).String()

	// Seed the alert + one explicit subscriber, tenant-correctly.
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO saved_searches (id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at)
			VALUES ($1, $2, $3, 'contracts', 'renewal', '{}'::jsonb, true, 15, now())`,
			ssID, tenant, owner); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO saved_search_subscribers (tenant_id, saved_search_id, user_id, channels, subscribed_by, subscribed_at)
			VALUES ($1, $2, $3, '{in_app,email}', $4, now())`,
			tenant, ssID, subscriber, owner)
		return err
	}))

	// Stub the search service: assert the owner-scoped identity headers
	// and return one matching document ("the ingested doc").
	var gotTenantHeader, gotUserHeader string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenantHeader = r.Header.Get("X-Auth-Tenant-ID")
		gotUserHeader = r.Header.Get("X-User-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{"document_id": matchedDoc}},
		})
	}))
	t.Cleanup(stub.Close)

	a := &activities.Activities{
		Pool:        db.App,
		Outbox:      database.NewOutboxRepository(),
		ServiceURLs: map[string]string{"search": stub.URL},
	}

	// 1. Load — must find the alert + subscribers under NOBYPASSRLS.
	loaded, err := a.LoadSavedSearchAlert(ctx, activities.LoadSavedSearchAlertInput{
		SavedSearchID: ssID.String(),
		TenantID:      tenant.String(),
	})
	require.NoError(t, err, "LoadSavedSearchAlert must see the row under prod posture (issue #70)")
	require.Equal(t, "renewal", loaded.Query)
	require.Equal(t, owner.String(), loaded.OwnerUserID)
	require.Len(t, loaded.Subscribers, 2, "explicit subscriber + implicitly-subscribed owner")

	// 2. Match — the search API is called with the OWNER's identity.
	docIDs, err := a.RunSavedSearchAlert(ctx, activities.RunSavedSearchInput{
		SavedSearchID: ssID.String(),
		TenantID:      tenant.String(),
		OwnerUserID:   loaded.OwnerUserID,
		Query:         loaded.Query,
	})
	require.NoError(t, err)
	require.Equal(t, []string{matchedDoc}, docIDs)
	require.Equal(t, tenant.String(), gotTenantHeader)
	require.Equal(t, owner.String(), gotUserHeader)

	// 3. Cursor — tenant-scoped write, verified by tenant-scoped read.
	require.NoError(t, a.UpdateSavedSearchAlertCursor(ctx, activities.UpdateCursorInput{
		SavedSearchID:   ssID.String(),
		TenantID:        tenant.String(),
		LastMatchDocIDs: docIDs,
	}))
	var cursorJSON []byte
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(last_match_doc_ids, '[]'::jsonb) FROM saved_searches WHERE id = $1`,
			ssID).Scan(&cursorJSON)
	}))
	var cursor []string
	require.NoError(t, json.Unmarshal(cursorJSON, &cursor))
	require.Equal(t, docIDs, cursor, "alert cursor must be written under prod posture")

	// 4. Fire — the alert lands in the transactional outbox for the
	// subscriber, tenant-scoped (the notification service consumes it
	// as dms.notify.saved_search_match.v1).
	require.NoError(t, a.EmitSavedSearchMatch(ctx, activities.EmitMatchInput{
		TenantID:        tenant.String(),
		SavedSearchID:   ssID.String(),
		SavedSearchName: "contracts",
		SubscriberID:    subscriber.String(),
		Channels:        []string{"in_app", "email"},
		MatchedDocIDs:   docIDs,
	}))

	var eventType string
	var eventTenant uuid.UUID
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT event_type, tenant_id FROM outbox WHERE aggregate_id = $1`,
		ssID).Scan(&eventType, &eventTenant))
	require.Equal(t, "dms.notify.saved_search_match.v1", eventType,
		"a matching ingested doc must fire the alert into the outbox")
	require.Equal(t, tenant, eventTenant)
}
