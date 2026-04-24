package service

// Pure-Go unit tests for the RecipientResolver seam. Hash-chain +
// canonicalJSON tests live in event_service_test.go; HMAC tests in
// attestation_service_test.go.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

func TestStaticRecipientResolver_DedupUsers(t *testing.T) {
	r := StaticRecipientResolver{}
	u1 := uuid.New()
	u2 := uuid.New()
	out, err := r.Resolve(context.Background(), uuid.New(), model.RecipientPolicy{
		Users: []uuid.UUID{u1, u2, u1},
	})
	require.NoError(t, err)
	require.Len(t, out, 2)
	require.Contains(t, out, u1)
	require.Contains(t, out, u2)
}

func TestStaticRecipientResolver_RejectsGroups(t *testing.T) {
	r := StaticRecipientResolver{}
	_, err := r.Resolve(context.Background(), uuid.New(), model.RecipientPolicy{
		Groups: []uuid.UUID{uuid.New()},
	})
	require.Error(t, err)
}
