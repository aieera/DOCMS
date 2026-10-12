//go:build integration

// D-25: when the storage pipeline is wired but the blob upload fails, the
// m365 ingest logged the error and still answered 201 — the add-in told the
// user "saved" while the documents were empty shells that the orphan GC
// then soft-deleted a day later. A failed upload now rolls back every row
// the request created and answers 502.
package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// downStorage is a storage client whose upload pipeline is unavailable.
type downStorage struct{ sedocv1.StorageServiceClient }

func (downStorage) InitiateUpload(context.Context, *sedocv1.InitiateUploadRequest, ...grpc.CallOption) (*sedocv1.InitiateUploadResponse, error) {
	return nil, errors.New("storage unavailable")
}

type noopPutter struct{}

func (noopPutter) PutObject(context.Context, string, string, io.Reader, int64, string) error {
	return nil
}

func TestM365Ingest_UploadFailureNot201(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	svc := service.New(pool, repository.New(pool), allowPolicy{}, zerolog.Nop())
	tenant, user, wsID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan, primary_region)
		VALUES ($1, 'M365 Org', $2, 'standard', 'us-east-1')`, tenant, "m-"+tenant.String()[:8])
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id, email, display_name, role, status)
		VALUES ($1, $2, $3, 'M365 User', 'admin', 'active')`, tenant, user, "u-"+user.String()[:8]+"@t.local")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name, region_pin, created_by)
		VALUES ($1, $2, 'ws', 'us-east-1', $3)`, tenant, wsID, user)
	require.NoError(t, err)
	ictx := auth.WithUser(auth.SetTenantID(ctx, tenant), auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
	folder, err := svc.CreateFolder(ictx, &service.CreateFolderInput{WorkspaceID: wsID, Name: "Inbox"})
	require.NoError(t, err)

	h := &M365IngestHandler{pool: pool, svc: svc, storage: downStorage{}, s3: noopPutter{}, log: zerolog.Nop()}
	liveDocs := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM documents WHERE tenant_id = $1 AND deleted_at IS NULL`, tenant).Scan(&n))
		return n
	}

	for name, includeBody := range map[string]bool{"with body": true, "attachments only": false} {
		ib := includeBody
		reqBody, _ := json.Marshal(map[string]any{
			"subject": "Q3 numbers", "from": "sender@partner.com", "to": []string{"me@corp.com"},
			"body_html":    "<p>See attached.</p>",
			"include_body": ib,
			"workspace_id": wsID.String(), "folder_id": folder.ID.String(),
			"attachments": []map[string]string{
				{"name": "q3.pdf", "mime_type": "application/pdf", "content_b64": base64.StdEncoding.EncodeToString([]byte("pdf bytes"))},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/integrations/m365/ingest-email", bytes.NewReader(reqBody)).WithContext(ictx)
		rec := httptest.NewRecorder()
		h.ingestEmail(rec, req)

		require.Equal(t, http.StatusBadGateway, rec.Code, "%s: %s", name, rec.Body.String())
		require.Zero(t, liveDocs(), "%s: the rows created for this email are rolled back", name)
	}
}
