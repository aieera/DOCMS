//go:build integration

// CB-4 (auth half) — SMS/email MFA enrollment created a `pending` row that
// nothing ever activated (lookups require `active`), and an unconfigured
// sender was offered anyway. Enrollment now sends a verification code and
// a correct code flips the method pending → active; methods whose sender
// is not available are hidden from the picker and refused at enrollment.
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/notifications"
	"github.com/aieera/sedoc/services/auth/internal/testenv"
)

func methodStatus(t *testing.T, tenant, user uuid.UUID, method string) string {
	t.Helper()
	var st string
	err := integrationEnv(t).Pool.QueryRow(context.Background(),
		`SELECT status FROM user_mfa_methods WHERE tenant_id = $1 AND user_id = $2 AND method = $3`,
		tenant, user, method).Scan(&st)
	require.NoError(t, err)
	return st
}

func enrolledHas(list []EnrolledMethod, m MFAMethod) bool {
	for _, e := range list {
		if e.Method == m {
			return true
		}
	}
	return false
}

func newEnrollmentUser(t *testing.T) (*Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	env := integrationEnv(t)
	ctx := testCtx(t)
	tenant := testenv.SeedTenant(ctx, t, env.Pool, testenv.UniqueSlug("enroll"))
	user := testenv.SeedUser(ctx, t, env.Pool, tenant, testenv.UserSpec{Email: "enroll-" + uuid.NewString()[:8] + "@example.com"})
	return newIntegrationService(t, env), tenant, user
}

func TestMFAEnrollment_ActivatesOnVerify(t *testing.T) {
	svc, tenant, user := newEnrollmentUser(t)
	ctx := testCtx(t)
	var sent string
	svc.SetMFADeps(MFADeps{
		Email: notifications.NewEmailOTPSender(notifications.EmailOTPConfig{DevStub: true, StubLog: func(_, code string) { sent = code }}),
		SMS:   notifications.NewSMSSender(notifications.SMSConfig{DevStub: true}),
	})

	// Email: enroll → code sent → wrong code keeps it pending → right code activates.
	require.NoError(t, svc.EnrollEmail(ctx, tenant, user, "second@example.com"))
	require.Len(t, sent, 6, "enrollment must send a verification code")
	require.Equal(t, "pending", methodStatus(t, tenant, user, "email"))
	wrong := "000000"
	if sent == wrong {
		wrong = "111111"
	}
	require.Error(t, svc.VerifyEmailEnrollment(ctx, tenant, user, wrong))
	require.Equal(t, "pending", methodStatus(t, tenant, user, "email"))
	require.NoError(t, svc.VerifyEmailEnrollment(ctx, tenant, user, sent))
	require.Equal(t, "active", methodStatus(t, tenant, user, "email"))

	// SMS (dev stub: Twilio's role played by the fixed stub code).
	require.NoError(t, svc.EnrollSMS(ctx, tenant, user, "+14155552671"))
	require.Equal(t, "pending", methodStatus(t, tenant, user, "sms"))
	require.NoError(t, svc.VerifySMSEnrollment(ctx, tenant, user, "000000"))
	require.Equal(t, "active", methodStatus(t, tenant, user, "sms"))

	methods, err := svc.ListEnrolledMethods(ctx, tenant, user)
	require.NoError(t, err)
	require.True(t, enrolledHas(methods, MethodEmail), "activated email factor is offered")
	require.True(t, enrolledHas(methods, MethodSMS), "activated sms factor is offered")
}

func TestMFAMethods_UnconfiguredHidden(t *testing.T) {
	svc, tenant, user := newEnrollmentUser(t)
	ctx := testCtx(t)
	_, err := integrationEnv(t).Pool.Exec(ctx, `
		INSERT INTO user_mfa_methods (tenant_id, user_id, method, status, phone_e164)
		VALUES ($1, $2, 'sms', 'active', '+14155552671')`, tenant, user)
	require.NoError(t, err)
	// Deployment without Twilio credentials and without SEDOC_MFA_DEV_STUB.
	svc.SetMFADeps(MFADeps{
		SMS:   notifications.NewSMSSender(notifications.SMSConfig{}),
		Email: notifications.NewEmailOTPSender(notifications.EmailOTPConfig{}),
	})

	methods, err := svc.ListEnrolledMethods(ctx, tenant, user)
	require.NoError(t, err)
	require.False(t, enrolledHas(methods, MethodSMS), "an SMS factor with no working sender must not be offered")

	require.ErrorIs(t, svc.EnrollEmail(ctx, tenant, user, "x@example.com"), ErrMethodUnavailable)
	require.ErrorIs(t, svc.EnrollSMS(ctx, tenant, user, "+14155550000"), ErrMethodUnavailable)
}
