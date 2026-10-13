//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/audit/internal/repository"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

// H-12 round 3: an erasure over a chain that does not verify (tampering,
// or a chain written before the canonical-hash fix) is refused with 409
// and a message that points at the re-baseline tool, not an opaque 500.
func TestAuditHandler_AnonymizeTamperedChain409(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	m, err := migrate.New("file://../../../../services/document/migrations", dsn)
	require.NoError(t, err)
	require.NoError(t, m.Steps(1))
	_, _ = m.Close()
	require.NoError(t, database.RunServiceMigrations(dsn, "../../migrations", "audit"))
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	svc := service.New(service.Config{Repo: repository.New(pool), Logger: zerolog.Nop()})
	tenant, subject := uuid.New(), uuid.NewString()
	for i := 0; i < 2; i++ {
		env, _ := json.Marshal(map[string]any{"tenantid": tenant.String(), "type": "document.viewed", "vdmsactorid": subject})
		require.NoError(t, svc.IngestEvent(ctx, "dms.document.viewed.v1", env))
	}
	_, err = pool.Exec(ctx, `UPDATE audit_events SET action = 'x' WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)

	mux := http.NewServeMux()
	New(svc, zerolog.Nop()).Register(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/audit/data-subject/anonymize",
		bytes.NewBufferString(`{"subject_id":"`+subject+`"}`))
	r = r.WithContext(auth.WithUser(r.Context(), auth.UserInfo{ID: uuid.New(), TenantID: tenant, Role: "admin"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "audit_chain_unverifiable", body["error"])
	require.Equal(t, "audit chain for this tenant cannot be verified from the subject's first event; "+
		"run the audit re-baseline (services/audit/cmd/rebaseline) if the chain predates the "+
		"canonical-hash fix, otherwise investigate tampering", body["message"])
}
