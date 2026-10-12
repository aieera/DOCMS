//go:build integration

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
)

// svcFixture is a Service on the dms_app (NOBYPASSRLS) pool over a migrated
// database with one tenant and one document version.
type svcFixture struct {
	db     *testutil.ProdPostureDB
	svc    *Service
	tenant repotest.Tenant
	doc    uuid.UUID
	ver    uuid.UUID
}

func newSvcFixture(t *testing.T) *svcFixture {
	t.Helper()
	db := repotest.New(t)
	ctx := context.Background()
	tn := repotest.SeedTenant(ctx, t, db.Super)
	doc, ver := repotest.SeedDocument(ctx, t, db.Super, tn.ID, tn.Owner)
	svc := New(Config{
		Pool: db.App, Repo: repository.New(db.App),
		Outbox: database.NewOutboxRepository(), Logger: zerolog.Nop(),
	})
	return &svcFixture{db: db, svc: svc, tenant: tn, doc: doc, ver: ver}
}

// createRequest mints a request as the tenant owner. provider "" = internal.
func (f *svcFixture) createRequest(t *testing.T, provider string, signers ...model.Signer) *model.SignatureRequest {
	t.Helper()
	if provider == "" {
		provider = "internal"
	}
	req, err := f.svc.CreateRequest(context.Background(), f.tenant.ID.String(), f.doc.String(), f.ver.String(),
		f.tenant.Owner.String(), provider, "remote", signers)
	require.NoError(t, err)
	return req
}

func tokenOf(t *testing.T, s model.Signer) string {
	t.Helper()
	_, tok, ok := strings.Cut(s.SigningURL, "token=")
	require.True(t, ok, "signer has no signing token: %q", s.SigningURL)
	return tok
}

func (f *svcFixture) status(t *testing.T, requestID string) string {
	t.Helper()
	var st string
	require.NoError(t, f.db.Super.QueryRow(context.Background(),
		`SELECT status FROM signature_requests WHERE tenant_id = $1 AND id = $2`, f.tenant.ID, requestID).Scan(&st))
	return st
}

func (f *svcFixture) outboxCount(t *testing.T, eventType string) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Super.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE tenant_id = $1 AND event_type = $2`, f.tenant.ID, eventType).Scan(&n))
	return n
}
