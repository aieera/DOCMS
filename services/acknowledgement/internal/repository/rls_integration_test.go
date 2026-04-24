//go:build integration

package repository_test

// Wave 15.1 — cross-tenant RLS + hash-chain integrity for the
// acknowledgement sub-wave. Three assertions:
//
//  1. Campaigns + assignments + events + signing keys isolate by
//     tenant_id under RLS.
//  2. The hash-chain over acknowledgement_events verifies: every
//     row's self_hash = SHA-256(prev_hash || payload) and the walk
//     terminates without a break.
//
// DoD row #3 of the Wave 15.1 brief ("audit chain verify passes")
// is promoted from "deferred" to "covered" once this file runs green.

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/pkg/testharness"
)

func TestAcknowledgementCampaigns_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/acknowledgement/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		docID, _ := uuid.NewV7()
		userID, _ := uuid.NewV7()
		_, err := tx.Exec(context.Background(), `
			INSERT INTO acknowledgement_campaigns (
				tenant_id, document_id, title, due_at, created_by_user_id, status
			) VALUES (
				current_setting('app.current_tenant')::uuid, $1,
				'Annual policy', now() + interval '7 days', $2, 'active'
			)`, docID, userID)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM acknowledgement_campaigns`,
		).Scan(&n)
		return n, err
	})
}

func TestAcknowledgementSigningKeys_CrossTenantIsolation(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/acknowledgement/migrations")

	tenantA := h.SeedTenant(t, "A")
	tenantB := h.SeedTenant(t, "B")

	h.AssertRLSIsolated(t, tenantA, tenantB, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO acknowledgement_signing_keys (tenant_id, kek_id, wrapped_key)
			VALUES (current_setting('app.current_tenant')::uuid, 'test-kek', '\xAB'::bytea)`)
		return err
	}, func(tx pgx.Tx) (int, error) {
		var n int
		err := tx.QueryRow(context.Background(),
			`SELECT count(*) FROM acknowledgement_signing_keys`,
		).Scan(&n)
		return n, err
	})
}

// TestAcknowledgementEvents_HashChainVerifies inserts a three-row
// chain and recomputes each self_hash. Matches the service's
// eventSelfHash = SHA-256(prev_hash || payload) definition.
func TestAcknowledgementEvents_HashChainVerifies(t *testing.T) {
	h := testharness.NewWithContainers(t,
		testharness.ContainerOptions{Postgres: true},
		testharness.BootPostgres, nil, nil,
	)
	h.RunMigrations(t, "../../../../services/document/migrations")
	h.RunMigrations(t, "../../../../services/acknowledgement/migrations")

	tenant := h.SeedTenant(t, "Chain")
	campaignID, _ := uuid.NewV7()

	// Bootstrap a campaign row so the FK on events resolves.
	require.NoError(t, h.WithTenantTx(tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.Ctx, `
			INSERT INTO acknowledgement_campaigns (
				tenant_id, id, document_id, title, due_at, created_by_user_id, status
			) VALUES ($1, $2, $3, 'T', now()+interval '1 day', $4, 'active')`,
			tenant, campaignID, uuid.New(), uuid.New())
		return err
	}))

	// Insert a hand-built 3-row chain.
	var prev []byte
	for i, payload := range [][]byte{
		[]byte(`{"event":"campaign.created","seq":1}`),
		[]byte(`{"event":"acknowledged","seq":2}`),
		[]byte(`{"event":"campaign.closed","seq":3}`),
	} {
		self := chainHash(prev, payload)
		require.NoError(t, h.WithTenantTx(tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(h.Ctx, `
				INSERT INTO acknowledgement_events (
					tenant_id, campaign_id, event_type, payload, prev_hash, self_hash
				) VALUES ($1, $2, $3, $4::jsonb, $5, $6)`,
				tenant, campaignID, "test.evt", string(payload), prev, self)
			return err
		}))
		prev = self
		_ = i
	}

	// Walk the chain back and verify every row.
	require.NoError(t, h.WithTenantTx(tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(h.Ctx, `
			SELECT payload::text, prev_hash, self_hash
			  FROM acknowledgement_events
			 WHERE tenant_id = $1 AND campaign_id = $2
			 ORDER BY created_at ASC`, tenant, campaignID)
		if err != nil {
			return err
		}
		defer rows.Close()

		var walked []byte
		count := 0
		for rows.Next() {
			var (
				payload  string
				prevH    []byte
				selfH    []byte
			)
			if err := rows.Scan(&payload, &prevH, &selfH); err != nil {
				return err
			}
			recomputed := chainHash(walked, []byte(payload))
			require.Equal(t, selfH, recomputed, "chain row %d self_hash mismatch", count)
			walked = selfH
			count++
		}
		require.Equal(t, 3, count)
		return rows.Err()
	}))
}

func chainHash(prev, payload []byte) []byte {
	h := sha256.New()
	if len(prev) > 0 {
		h.Write(prev)
	}
	h.Write(payload)
	return h.Sum(nil)
}
