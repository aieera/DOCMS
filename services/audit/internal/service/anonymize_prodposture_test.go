//go:build integration && prodposture

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// H-12: GDPR anonymize could never succeed. It wrote ip_address = ” into
// an INET column (parse error in every environment) and UPDATEd
// audit_events, which is revoked from dms_app (WORM), so the re-chain
// failed too. The redaction and the re-chain now go through two SECURITY
// DEFINER functions owned by dms_audit_admin; dms_app itself still cannot
// UPDATE audit_events.
func TestProdPosture_AnonymizeSubjectRechains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	db := newProdPostureAuditDB(ctx, t)
	svc := New(Config{Repo: repository.New(db.App), Logger: zerolog.Nop(), SigningKeySeedB64: newSeedB64()})

	tenant := uuid.Must(uuid.NewV7())
	subject := uuid.Must(uuid.NewV7()).String()
	other := uuid.Must(uuid.NewV7()).String()

	ingest := func(actor, ip string) {
		env, _ := json.Marshal(map[string]any{
			"tenantid":      tenant.String(),
			"type":          "document.viewed",
			"vdmsactorid":   actor,
			"vdmsactorname": "name-of-" + actor,
			"vdmsclientip":  ip,
			"vdmsuseragent": "UA/" + actor,
			"data":          map[string]any{"resource_type": "document", "resource_id": uuid.Must(uuid.NewV7()).String()},
		})
		require.NoError(t, svc.IngestEvent(ctx, "dms.document.viewed.v1", env))
	}
	ingest(other, "198.51.100.1")
	ingest(subject, "203.0.113.7")
	ingest(other, "198.51.100.2")
	ingest(subject, "203.0.113.8")
	ingest(other, "198.51.100.3")

	before, err := svc.CreateCheckpoint(ctx, tenant.String())
	require.NoError(t, err)

	n, err := svc.AnonymizeSubject(ctx, tenant.String(), subject)
	require.NoError(t, err, "anonymize must succeed under dms_app (NOBYPASSRLS, no UPDATE on audit_events)")
	require.EqualValues(t, 2, n)

	// The subject is gone from every column the redaction covers.
	var left, redacted int
	require.NoError(t, db.Super.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE actor = $2 OR actor_id::text = $2 OR details::text LIKE '%' || $2 || '%'),
		       count(*) FILTER (WHERE actor = 'REDACTED' AND actor_name = 'REDACTED' AND actor_id IS NULL
		                          AND ip_address IS NULL AND user_agent IS NULL)
		FROM audit_events WHERE tenant_id = $1`, tenant, subject).Scan(&left, &redacted))
	require.Zero(t, left, "no trace of the subject may remain")
	require.Equal(t, 2, redacted)

	// The chain verifies end to end and a NEW signed checkpoint anchors it.
	res, err := svc.VerifyIntegrity(ctx, tenant.String())
	require.NoError(t, err)
	require.True(t, res.Valid, "chain must verify after the re-chain (broken at %s)", res.BrokenAt)
	require.EqualValues(t, 5, res.Verified)
	require.True(t, res.CheckpointValid, res.CheckpointMessage)
	cps, err := svc.ListCheckpoints(ctx, tenant.String(), 10)
	require.NoError(t, err)
	require.Len(t, cps, 2)
	require.NotEqual(t, before.HeadHash, cps[0].HeadHash, "the head moved, so a new checkpoint must sign it")

	// WORM still holds for the app role itself.
	err = database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE audit_events SET action = 'x' WHERE tenant_id = $1`, tenant)
		return e
	})
	require.ErrorContains(t, err, "permission denied", "dms_app must not UPDATE audit_events directly")

	// The definers only act inside the matching tenant context.
	otherTenant := uuid.Must(uuid.NewV7())
	err = database.WithTenantTx(ctx, db.App, otherTenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `SELECT count(*) FROM audit_redact_subject($1::uuid, $2)`, tenant, other)
		return e
	})
	require.Error(t, err, "redacting another tenant's subject from a foreign tenant context must fail")
}
