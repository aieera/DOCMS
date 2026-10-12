//go:build integration && prodposture

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// H-12 follow-up 2: the erasure marker must name a real caller. An empty
// or non-uuid actor is a validation error (400 at the handler), never an
// unattributed marker or a 500 on the uuid cast.
func TestAnonymize_RequiresActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, svc := newAnonymizeFixture(ctx, t)
	tenant := uuid.Must(uuid.NewV7())
	subject, other := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	seedChain(ctx, t, svc, tenant, subject, other)

	for _, actor := range []string{"", "not-a-uuid"} {
		_, err := svc.AnonymizeSubject(ctx, tenant.String(), subject, actor, "dpo")
		require.ErrorIs(t, err, ErrActorRequired, "actor %q", actor)
	}
	var left int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor = $2`, tenant, subject).Scan(&left))
	require.Equal(t, 2, left)
}
