//go:build integration

// H-2: RecordSignature must refuse requests that are no longer signable
// (cancelled, expired, completed) and signers who already signed, and the
// completion write must be conditional so a race cannot complete a request
// that was cancelled in between.
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/signature/internal/model"
)

func twoSigners() []model.Signer {
	return []model.Signer{
		{Email: "a@test.local", Name: "A", Role: "signer", Order: 1},
		{Email: "b@test.local", Name: "B", Role: "signer", Order: 2},
	}
}

func (f *svcFixture) sign(req *model.SignatureRequest, i int) error {
	return f.svc.RecordSignature(context.Background(), RecordSignatureInput{
		TenantID: f.tenant.ID.String(), RequestID: req.ID, SignerID: req.Signers[i].ID,
		Token: tokenFromURL(req.Signers[i].SigningURL), IPAddress: "10.0.0.1",
	})
}

func tokenFromURL(u string) string { return extractSigningToken(u) }

func TestRecordSignature_CancelledRejected(t *testing.T) {
	f := newSvcFixture(t)
	req := f.createRequest(t, "", model.Signer{Email: "a@test.local", Name: "A", Role: "signer", Order: 1})
	require.NoError(t, f.svc.CancelRequest(context.Background(), f.tenant.ID.String(), req.ID, f.tenant.Owner.String(), false))

	err := f.sign(req, 0)
	require.True(t, errors.Is(err, ErrRequestNotSignable), "signing a cancelled request must be rejected, got %v", err)
	require.Equal(t, "cancelled", f.status(t, req.ID))
	require.Zero(t, f.outboxCount(t, "dms.signature.completed.v1"), "a cancelled request must never emit completion")
}

func TestRecordSignature_ExpiredRejected(t *testing.T) {
	f := newSvcFixture(t)
	req := f.createRequest(t, "", model.Signer{Email: "a@test.local", Name: "A", Role: "signer", Order: 1})
	_, err := f.db.Super.Exec(context.Background(),
		`UPDATE signature_requests SET expires_at = now() - interval '1 minute' WHERE tenant_id = $1 AND id = $2`,
		f.tenant.ID, req.ID)
	require.NoError(t, err)

	err = f.sign(req, 0)
	require.True(t, errors.Is(err, ErrRequestNotSignable), "signing an expired request must be rejected, got %v", err)
	require.Equal(t, "pending", f.status(t, req.ID))
	require.Zero(t, f.outboxCount(t, "dms.signature.completed.v1"))
}

func TestRecordSignature_ResignRejected(t *testing.T) {
	f := newSvcFixture(t)
	req := f.createRequest(t, "", twoSigners()...)

	require.NoError(t, f.sign(req, 0))
	err := f.sign(req, 0)
	require.True(t, errors.Is(err, ErrAlreadySigned), "a signer must not sign twice, got %v", err)
	require.Equal(t, "pending", f.status(t, req.ID))

	require.NoError(t, f.sign(req, 1))
	require.Equal(t, "completed", f.status(t, req.ID))
	require.Equal(t, 1, f.outboxCount(t, "dms.signature.completed.v1"))

	err = f.sign(req, 1)
	require.Error(t, err, "re-signing after completion must be rejected")
	require.Equal(t, 1, f.outboxCount(t, "dms.signature.completed.v1"), "completion must be emitted exactly once")

	err = f.svc.CancelRequest(context.Background(), f.tenant.ID.String(), req.ID, f.tenant.Owner.String(), false)
	require.True(t, errors.Is(err, ErrRequestNotSignable), "a completed request must not be cancellable, got %v", err)
	require.Equal(t, "completed", f.status(t, req.ID))
}
