//go:build integration && prodposture

// Prod-posture coverage for the iPaaS completed-signatures trigger feed.
// The handler queried signature_requests (FORCE RLS) on the raw pool, so
// under the production dms_app role the feed was always empty.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
)

func TestProdPosture_SignatureTriggersFeed(t *testing.T) {
	testutil.AssertProdPosture(t)
	db := repotest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	a := repotest.SeedTenant(ctx, t, db.Super)
	b := repotest.SeedTenant(ctx, t, db.Super)
	seedCompleted := func(tn repotest.Tenant) uuid.UUID {
		doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)
		id := uuid.Must(uuid.NewV7())
		_, err := db.Super.Exec(ctx, `
			INSERT INTO signature_requests (tenant_id, id, document_id, version_id, requested_by,
				status, provider, created_at, completed_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, 'completed', 'internal', now(), now(), now() + interval '1 day')`,
			tn.ID, id, doc, ver, tn.Owner)
		require.NoError(t, err)
		return id
	}
	reqA := seedCompleted(a)
	seedCompleted(b)

	mux := http.NewServeMux()
	NewIntegrationTriggersHandler(db.App).Register(mux)

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/triggers/signatures/completed?since="+since, nil)
	r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: a.Owner, TenantID: a.ID, Role: "api_key"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got []triggerSignature
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 1, "the feed must return the tenant's completed request under NOBYPASSRLS (and only that tenant's)")
	require.Equal(t, reqA.String(), got[0].ID)
}
