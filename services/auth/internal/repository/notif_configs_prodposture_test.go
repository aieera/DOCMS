//go:build integration && prodposture

// M-27 inventory — the per-tenant Twilio (SMS MFA) and SMTP (email OTP)
// credential tables are FORCE ROW LEVEL SECURITY, but the repository read
// and wrote them on the raw pool: under the production dms_app
// (NOBYPASSRLS) role admin saves were rejected and the MFA senders never
// saw a tenant's credentials. They now run in the tenant's transaction.
package repository_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func TestProdPosture_NotificationProviderConfigs(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, db := testenv.ProdPosture(t)
	tenant := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("notif"))
	other := testenv.SeedTenant(ctx, t, db.Super, testenv.UniqueSlug("notif-other"))
	db.App.Reset()

	repo := repository.New(db.App)
	tid := tenant.String()

	require.NoError(t, repo.UpsertTwilioConfig(ctx, &repository.TwilioConfig{
		TenantID: tid, AccountSID: "AC1", AuthTokenSealed: "sealed", VerifyServiceSID: "VA1",
	}), "twilio upsert under NOBYPASSRLS")
	tw, err := repo.GetTwilioConfig(ctx, tid)
	require.NoError(t, err)
	require.NotNil(t, tw, "twilio config must be readable in the tenant tx")
	require.Equal(t, "AC1", tw.AccountSID)
	otherTw, err := repo.GetTwilioConfig(ctx, other.String())
	require.NoError(t, err)
	require.Nil(t, otherTw, "another tenant must not see it")
	require.NoError(t, repo.DeleteTwilioConfig(ctx, tid))
	tw, err = repo.GetTwilioConfig(ctx, tid)
	require.NoError(t, err)
	require.Nil(t, tw, "delete must remove the row")

	require.NoError(t, repo.UpsertSMTPConfig(ctx, &repository.SMTPConfig{
		TenantID: tid, Host: "smtp.example", Port: 587, FromAddr: "noreply@example.com", StartTLS: true,
	}, ""), "smtp upsert under NOBYPASSRLS")
	sm, err := repo.GetSMTPConfig(ctx, tid)
	require.NoError(t, err)
	require.NotNil(t, sm, "smtp config must be readable in the tenant tx")
	require.Equal(t, "smtp.example", sm.Host)
	require.NoError(t, repo.DeleteSMTPConfig(ctx, tid))
	sm, err = repo.GetSMTPConfig(ctx, tid)
	require.NoError(t, err)
	require.Nil(t, sm)
}
