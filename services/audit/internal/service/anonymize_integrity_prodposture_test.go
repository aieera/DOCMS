//go:build integration && prodposture

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// seedChain ingests other/subject/other/subject/other for one tenant.
func seedChain(ctx context.Context, t *testing.T, svc *Service, tenant uuid.UUID, subject, other string) {
	t.Helper()
	for i, actor := range []string{other, subject, other, subject, other} {
		env, _ := json.Marshal(map[string]any{
			"tenantid": tenant.String(), "type": "document.viewed",
			"vdmsactorid": actor, "vdmsactorname": "name-of-" + actor,
			"data": map[string]any{"resource_type": "document", "resource_id": uuid.Must(uuid.NewV7()).String(), "i": i},
		})
		require.NoError(t, svc.IngestEvent(ctx, "dms.document.viewed.v1", env))
	}
}

func newAnonymizeFixture(ctx context.Context, t *testing.T) (*testutil.ProdPostureDB, *Service) {
	t.Helper()
	db := newProdPostureAuditDB(ctx, t)
	return db, New(Config{Repo: repository.New(db.App), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})
}

// H-12 follow-up: the re-chain rewrote every hash from the first redacted
// event onward without checking the events it was not asked to change, so
// the fresh signed checkpoint certified any earlier tampering. A
// non-redacted event in the rewritten range whose stored hash no longer
// matches its content must abort the whole erasure (nothing redacted,
// nothing re-chained).
func TestAnonymize_AbortsOnPriorTamper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, svc := newAnonymizeFixture(ctx, t)
	tenant := uuid.Must(uuid.NewV7())
	subject, other := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	seedChain(ctx, t, svc, tenant, subject, other)

	// Tamper with a non-redacted event AFTER the first redacted one
	// (out-of-band, as a DB-level attacker would).
	_, err := db.Super.Exec(ctx, `
		UPDATE audit_events SET action = 'document.deleted'
		WHERE id = (SELECT id FROM audit_events WHERE tenant_id = $1 AND actor = $2
		            ORDER BY created_at DESC, id DESC LIMIT 1)`, tenant, other)
	require.NoError(t, err)

	_, err = svc.AnonymizeSubject(ctx, tenant.String(), subject, uuid.Must(uuid.NewV7()).String(), "dpo")
	require.Error(t, err, "erasure must refuse to re-certify a tampered chain")

	var stillThere int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor = $2`, tenant, subject).Scan(&stillThere))
	require.Equal(t, 2, stillThere, "the aborted erasure must roll back completely")
	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.False(t, res.Valid, "the tamper must still be visible to verification")
}

// H-12 follow-up: an authorised erasure must be distinguishable from
// tampering — it appends an audit.subject_erased event to the chain in the
// same transaction, naming the caller and carrying only a hash of the
// subject plus the number of events redacted.
func TestAnonymize_AppendsErasureMarker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, svc := newAnonymizeFixture(ctx, t)
	tenant := uuid.Must(uuid.NewV7())
	subject, other := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
	caller := uuid.Must(uuid.NewV7()).String()
	seedChain(ctx, t, svc, tenant, subject, other)

	n, err := svc.AnonymizeSubject(ctx, tenant.String(), subject, caller, "dpo@example.com")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)

	var actor, actorName, details string
	require.NoError(t, db.Super.QueryRow(ctx, `
		SELECT actor, actor_name, details::text FROM audit_events
		WHERE tenant_id = $1 AND action = 'audit.subject_erased'`, tenant).Scan(&actor, &actorName, &details))
	require.Equal(t, caller, actor)
	require.Equal(t, "dpo@example.com", actorName)
	var d struct {
		SubjectHash   string `json:"subject_hash"`
		RedactedCount int    `json:"redacted_count"`
	}
	require.NoError(t, json.Unmarshal([]byte(details), &d))
	sum := sha256.Sum256([]byte(subject))
	require.Equal(t, hex.EncodeToString(sum[:]), d.SubjectHash)
	require.Equal(t, 2, d.RedactedCount)
	require.NotContains(t, details, subject, "the marker must not re-introduce the erased identifier")

	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.True(t, res.Valid, "broken at %s", res.BrokenAt)
	require.EqualValues(t, 6, res.Verified, "5 events + the erasure marker")
	require.True(t, res.CheckpointValid, res.CheckpointMessage)
}
