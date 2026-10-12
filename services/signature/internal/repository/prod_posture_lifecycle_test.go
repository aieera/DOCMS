//go:build integration && prodposture

// Prod-posture coverage for the signature repository (review finding M-15).
//
// Every signature table is FORCE ROW LEVEL SECURITY keyed on
// app.current_tenant. The repository used to read and write on the raw pool,
// which works only for the dev superuser (BYPASSRLS): under the production
// dms_app role every raw-pool read returns zero rows and every raw-pool write
// is rejected by the WITH CHECK policy. This test drives the repository's
// public surface through the dms_app pool and asserts the post-fix
// behaviour, including tenant isolation and the cross-tenant sweeps.
package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/repository"
	"github.com/aieera/sedoc/services/signature/internal/repository/repotest"
)

func newRequest(tenantID, docID, versionID, createdBy uuid.UUID) *model.SignatureRequest {
	return &model.SignatureRequest{
		ID:          repository.NewID(),
		TenantID:    tenantID.String(),
		DocumentID:  docID.String(),
		VersionID:   versionID.String(),
		CreatedBy:   createdBy.String(),
		Status:      "pending",
		Provider:    "internal",
		SigningMode: "remote",
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(24 * time.Hour),
		Signers: []model.Signer{{
			ID: repository.NewID(), Email: "s1@test.local", Name: "S1", Role: "signer", Order: 1,
			SigningURL: "/sign/x/y?token=" + uuid.NewString(),
		}},
	}
}

func TestProdPosture_SignatureRequestLifecycle(t *testing.T) {
	testutil.AssertProdPosture(t)
	db := repotest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	a := repotest.SeedTenant(ctx, t, db.Super)
	b := repotest.SeedTenant(ctx, t, db.Super)
	docA, verA := repotest.SeedDocument(ctx, t, db.Super, a.ID, a.Owner)
	docB, verB := repotest.SeedDocument(ctx, t, db.Super, b.ID, b.Owner)

	repo := repository.New(db.App)
	reqA := newRequest(a.ID, docA, verA, a.Owner)
	reqB := newRequest(b.ID, docB, verB, b.Owner)

	t.Run("create and read back under dms_app", func(t *testing.T) {
		require.NoError(t, repo.Create(ctx, reqA))
		require.NoError(t, repo.Create(ctx, reqB))

		got, err := repo.GetByID(ctx, a.ID.String(), reqA.ID)
		require.NoError(t, err)
		require.NotNil(t, got, "GetByID must see the tenant's own request under NOBYPASSRLS")
		require.Equal(t, "pending", got.Status)
		require.Len(t, got.Signers, 1)

		list, err := repo.ListByDocument(ctx, a.ID.String(), docA.String())
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Len(t, list[0].Signers, 1)

		tok, err := repo.SignerSigningToken(ctx, a.ID.String(), reqA.ID, reqA.Signers[0].ID)
		require.NoError(t, err)
		require.Equal(t, reqA.Signers[0].SigningURL, tok)
	})

	t.Run("tenant isolation", func(t *testing.T) {
		got, err := repo.GetByID(ctx, b.ID.String(), reqA.ID)
		require.NoError(t, err)
		require.Nil(t, got, "tenant B must not read tenant A's request")
		list, err := repo.ListByDocument(ctx, b.ID.String(), docA.String())
		require.NoError(t, err)
		require.Empty(t, list)
		err = repo.UpdateStatus(ctx, b.ID.String(), reqA.ID, "cancelled")
		require.True(t, errors.Is(err, repository.ErrNotFound), "cross-tenant UpdateStatus must report not found, got %v", err)
	})

	t.Run("update status checks rows affected", func(t *testing.T) {
		require.NoError(t, repo.UpdateStatus(ctx, a.ID.String(), reqA.ID, "cancelled"))
		got, err := repo.GetByID(ctx, a.ID.String(), reqA.ID)
		require.NoError(t, err)
		require.Equal(t, "cancelled", got.Status)

		err = repo.UpdateStatus(ctx, a.ID.String(), repository.NewID(), "cancelled")
		require.True(t, errors.Is(err, repository.ErrNotFound), "unknown request must be ErrNotFound, got %v", err)
	})

	t.Run("esign tokens and provider configs", func(t *testing.T) {
		tok := &repository.ESignToken{
			TenantID: a.ID.String(), Provider: "docusign",
			AccessToken: "sealed-access", RefreshToken: "sealed-refresh",
			ExpiresAt: time.Now().Add(time.Hour), AccountID: "acc", BaseURI: "https://demo.docusign.net",
			ConnectedBy: a.Owner.String(), ConnectedAt: time.Now(), UpdatedAt: time.Now(),
		}
		require.NoError(t, repo.UpsertESignToken(ctx, tok))
		got, err := repo.GetESignToken(ctx, a.ID.String(), "docusign")
		require.NoError(t, err)
		require.NotNil(t, got)
		list, err := repo.ListESignTokens(ctx, a.ID.String())
		require.NoError(t, err)
		require.Len(t, list, 1)
		other, err := repo.GetESignToken(ctx, b.ID.String(), "docusign")
		require.NoError(t, err)
		require.Nil(t, other)

		due, err := repo.ListESignTokensExpiringWithin(ctx, 24*time.Hour)
		require.NoError(t, err)
		require.Len(t, due, 1, "refresh sweep must see every tenant's due tokens under NOBYPASSRLS")
		require.Equal(t, a.ID.String(), due[0].TenantID)

		require.NoError(t, repo.DeleteESignToken(ctx, a.ID.String(), "docusign"))
		got, err = repo.GetESignToken(ctx, a.ID.String(), "docusign")
		require.NoError(t, err)
		require.Nil(t, got)

		cfg := &repository.ESignProviderConfig{
			TenantID: a.ID.String(), Provider: "docusign", ClientID: "cid",
			ClientSecretSealed: "sealed", Environment: "sandbox", ConfiguredBy: a.Owner.String(),
		}
		require.NoError(t, repo.UpsertESignProviderConfig(ctx, cfg))
		gotCfg, err := repo.GetESignProviderConfig(ctx, a.ID.String(), "docusign")
		require.NoError(t, err)
		require.NotNil(t, gotCfg)
		require.NoError(t, repo.DeleteESignProviderConfig(ctx, a.ID.String(), "docusign"))
		gotCfg, err = repo.GetESignProviderConfig(ctx, a.ID.String(), "docusign")
		require.NoError(t, err)
		require.Nil(t, gotCfg)
	})

	t.Run("envelope lookup and stale sweep", func(t *testing.T) {
		vend := newRequest(b.ID, docB, verB, b.Owner)
		vend.Provider = "docusign"
		vend.Signers[0].SigningURL = ""
		vend.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
		require.NoError(t, repo.Create(ctx, vend))
		require.NoError(t, database.WithTenantTx(ctx, db.App, b.ID, func(tx pgx.Tx) error {
			return repo.SetEnvelopeIDTx(ctx, tx, b.ID.String(), vend.ID, "env-1")
		}))
		id, err := repo.FindRequestByEnvelope(ctx, b.ID.String(), "env-1")
		require.NoError(t, err)
		require.Equal(t, vend.ID, id)
		id, err = repo.FindRequestByEnvelope(ctx, a.ID.String(), "env-1")
		require.NoError(t, err)
		require.Empty(t, id, "envelope lookup must stay tenant-scoped")

		stale, err := repo.FindStaleInProgressRequests(ctx, time.Hour)
		require.NoError(t, err)
		require.Len(t, stale, 1, "reconciler sweep must see in-progress envelopes under NOBYPASSRLS")
		require.Equal(t, b.ID.String(), stale[0].TenantID)
	})

	t.Run("qes sessions and reaper", func(t *testing.T) {
		sess := &repository.TSPSession{
			ID: repository.NewID(), TenantID: b.ID.String(), RequestID: reqB.ID, SignerID: reqB.Signers[0].ID,
			Provider: "swisscom", Status: "pending", DocumentHash: "abc", ExternalID: "ext",
			RedirectURL: "https://tsp", ReturnURL: "https://ret", StateSecret: "s",
			CreatedAt: time.Now().UTC().Add(-time.Hour), ExpiresAt: time.Now().UTC().Add(-time.Minute),
		}
		require.NoError(t, database.WithTenantTx(ctx, db.App, b.ID, func(tx pgx.Tx) error {
			return repo.CreateTSPSessionTx(ctx, tx, sess)
		}))
		got, err := repo.GetTSPSession(ctx, b.ID.String(), sess.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		n, err := repo.ReapExpiredTSPSessions(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 1, n, "QES reaper must expire every tenant's stale sessions under NOBYPASSRLS")
		require.NoError(t, repo.MarkTSPSessionFailed(ctx, b.ID.String(), sess.ID, "x"))
		got, err = repo.GetTSPSession(ctx, b.ID.String(), sess.ID)
		require.NoError(t, err)
		require.Equal(t, "failed", got.Status)
		certs, err := repo.GetQESCertificateByRequest(ctx, b.ID.String(), reqB.ID)
		require.NoError(t, err)
		require.Empty(t, certs)
	})
}
