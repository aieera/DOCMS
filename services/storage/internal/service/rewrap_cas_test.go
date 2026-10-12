//go:build integration
// +build integration

// D-18 — blob row updates must be compare-and-set.
//
// RewrapDEK read the row, unwrapped/re-wrapped the DEK, and wrote the WHOLE
// row snapshot back through UpdateMigration (bucket, key, nonce, envelope)
// with no condition. A re-encrypt that committed in between (new key, new
// nonce, new DEK, old object deleted) was silently reverted: the row
// pointed back at the deleted object with the old nonce — the blob was
// gone. RewrapDEK now updates only the envelope columns, conditioned on the
// envelope + nonce it read; UpdateMigration is conditioned on the location
// + envelope it read. A lost race is a Conflict and changes nothing.
package service

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

// hookKMS runs hook once, just before the first EncryptDataKey — i.e. in
// the middle of RewrapDEK, after it has read the row.
type hookKMS struct {
	pkgcrypto.KeyManager
	once sync.Once
	hook func()
}

func (k *hookKMS) EncryptDataKey(ctx context.Context, kekID string, dek []byte) ([]byte, error) {
	k.once.Do(k.hook)
	return k.KeyManager.EncryptDataKey(ctx, kekID, dek)
}

func TestRewrap_ConcurrentReencryptConflict(t *testing.T) {
	h := newHarness(t, harnessConfig{encrypt: true})
	payload := []byte("rewrap must not resurrect a re-encrypted blob's old location")
	blobID := uploadEncrypted(t, h, "race.txt", payload) // under the base alias (no tenant_keks yet)

	// Rotate: v1 (base) retired, v2 live → RewrapDEK has work to do.
	base := aliasForTenant(h.tenant)
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO tenant_keks (tenant_id, version, alias, retired_at) VALUES
			($1, 1, $2, now()), ($1, 2, $3, NULL)`, h.tenant, base, base+"@v2")
	require.NoError(t, err)

	var reencErr error
	kms := &hookKMS{KeyManager: h.kms, hook: func() {
		// A region re-key commits while the rewrap is mid-flight.
		_, reencErr = h.svc.ReencryptBlob(h.ctx, ReencryptBlobInput{
			TenantID: h.tenant, BlobID: blobID, TargetRegion: "us-east-1",
		})
	}}
	rewrapper := New(Config{
		Pool: h.pool, Repos: repository.New(h.pool), S3: h.s3,
		Outbox: database.NewOutboxRepository(), KMS: kms,
		Logger: zerolog.Nop(), DefaultRegion: "us-east-1",
	})

	_, err = rewrapper.RewrapDEK(h.ctx, RewrapDEKInput{TenantID: h.tenant, BlobID: blobID})
	require.NoError(t, reencErr, "the concurrent re-encrypt itself succeeds")
	require.Error(t, err, "the stale rewrap must lose the race")
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "got %v", err)

	after := loadBlob(t, h, blobID)
	require.Equal(t, aliasForTenantInRegion(h.tenant, "us-east-1"), after.KEKID, "the re-encrypt's envelope stands")
	got, err := readBlob(t, h, blobID)
	require.NoError(t, err, "the blob must remain readable after the lost rewrap")
	require.Equal(t, payload, got)
}
