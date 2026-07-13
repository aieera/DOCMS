//go:build integration
// +build integration

// Durable SAML SP identity (the fix for the every-boot cert regenerate).
//
// Two sequential "boots" must present the SAME SP cert fingerprint (so a
// pinning IdP doesn't break on restart), and rotation must produce a
// controlled, different fingerprint. Runs against real Postgres.
package sso

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

const spKeypairDDL = `
CREATE TABLE saml_sp_keypair (
	id                 BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
	private_key_sealed BYTEA       NOT NULL,
	certificate_pem    TEXT        NOT NULL,
	fingerprint        TEXT        NOT NULL,
	created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
	rotated_at         TIMESTAMPTZ
);`

func spTestKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*7 + 1)
	}
	return k
}

func newSPStorePool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn, clean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(clean)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, spKeypairDDL)
	require.NoError(t, err)
	return ctx, pool
}

func TestSPKeypair_StableAcrossBoots(t *testing.T) {
	ctx, pool := newSPStorePool(t)
	kek := spTestKEK()

	// Boot 1 — generates + persists.
	m1, created1, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
	require.NoError(t, err)
	require.True(t, created1, "first boot must generate the keypair")
	fp1 := CertFingerprint(m1.Certificate)

	// Boot 2 — must load the SAME keypair (no regenerate).
	m2, created2, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
	require.NoError(t, err)
	require.False(t, created2, "second boot must NOT regenerate")
	fp2 := CertFingerprint(m2.Certificate)

	require.Equal(t, fp1, fp2, "SP cert fingerprint must be stable across restarts")
	// The private key round-trips too (same modulus).
	require.Equal(t, 0, m1.PrivateKey.N.Cmp(m2.PrivateKey.N), "private key must be identical across boots")
	require.Equal(t, m1.Certificate.Raw, m2.Certificate.Raw)
}

func TestSPKeypair_ConcurrentFirstBootConverges(t *testing.T) {
	ctx, pool := newSPStorePool(t)
	kek := spTestKEK()

	// Simulate N replicas booting at once on an empty table.
	const n = 6
	fps := make([]string, n)
	errs := make([]error, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			m, _, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
			if err == nil {
				fps[i] = CertFingerprint(m.Certificate)
			}
			errs[i] = err
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
	}
	// Every replica must converge on the SAME keypair (ON CONFLICT race).
	for i := 1; i < n; i++ {
		require.Equal(t, fps[0], fps[i], "all concurrent boots must resolve the same SP cert")
	}
	// Exactly one row exists.
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM saml_sp_keypair`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestSPKeypair_RotationIsControlledChange(t *testing.T) {
	ctx, pool := newSPStorePool(t)
	kek := spTestKEK()

	m1, _, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
	require.NoError(t, err)
	fp1 := CertFingerprint(m1.Certificate)

	// Rotate → NEW fingerprint, rotated_at set.
	m2, err := RotateSPKeyMaterial(ctx, pool, kek)
	require.NoError(t, err)
	fp2 := CertFingerprint(m2.Certificate)
	require.NotEqual(t, fp1, fp2, "rotation must produce a different SP cert")

	var rotated *time.Time
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT rotated_at, count(*) OVER () FROM saml_sp_keypair`).Scan(&rotated, &count))
	require.NotNil(t, rotated, "rotated_at must be set after rotation")
	require.Equal(t, 1, count, "rotation overwrites the singleton, not appends")

	// Subsequent boots now present the ROTATED cert, stably.
	m3, created, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, fp2, CertFingerprint(m3.Certificate), "post-rotation boots serve the new cert")
}

func TestSPKeypair_WrongKEKFailsClosed(t *testing.T) {
	ctx, pool := newSPStorePool(t)
	_, _, err := LoadOrCreateSPKeyMaterial(ctx, pool, spTestKEK())
	require.NoError(t, err)

	// A different KEK can't unseal the stored key → error, not a silent
	// wrong result.
	badKEK := make([]byte, 32)
	for i := range badKEK {
		badKEK[i] = 0xAB
	}
	_, _, err = LoadOrCreateSPKeyMaterial(ctx, pool, badKEK)
	require.Error(t, err, "unsealing with the wrong KEK must fail")
}
