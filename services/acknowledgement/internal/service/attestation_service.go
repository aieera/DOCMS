package service

// attestation_service.go — per-tenant HMAC key (wrapped by KMS at
// rest), plaintext cache keyed by tenant, attestation roundtrip.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// attestationHMAC derives a per-tenant key material from the KEK,
// then HMAC-SHA256s (campaign|user|timestamp). The KEK is wrapped and
// NEVER logged.
func (s *Service) attestationHMAC(ctx context.Context, tenantID, campaignID, userID uuid.UUID, at time.Time) ([]byte, error) {
	// Derive a 32-byte signing key from the tenant KEK. The crypto
	// package exposes GetOrCreateTenantMaterial which handles envelope
	// wrapping + caching.
	key, err := s.resolveTenantHMACKey(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("attestation key: %w", err)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(campaignID.String()))
	h.Write([]byte("|"))
	h.Write([]byte(userID.String()))
	h.Write([]byte("|"))
	h.Write([]byte(at.Format(time.RFC3339Nano)))
	return h.Sum(nil), nil
}

// InvalidateTenantSigningKey drops the cached plaintext signing key
// for a tenant. Call on KEK rotation so the next request re-unwraps
// via the new KEK id. Safe to call for tenants that aren't cached.
func (s *Service) InvalidateTenantSigningKey(tenantID uuid.UUID) {
	s.keyCacheMu.Lock()
	delete(s.keyCache, tenantID)
	s.keyCacheMu.Unlock()
}

// tenantKEKID returns the well-known KEK id for a tenant's
// acknowledgement signing key. Namespaced so it can't collide with
// other per-tenant keys (documents, MFA secrets, etc).
func tenantKEKID(tenantID uuid.UUID) string {
	return "vaultdms/tenant/" + tenantID.String() + "/acknowledgement"
}

// resolveTenantHMACKey returns the 32-byte HMAC key for the tenant.
// Creates-and-stores on first use; unwraps-and-caches on subsequent
// use. The plaintext key NEVER touches logs or the outbox.
func (s *Service) resolveTenantHMACKey(ctx context.Context, tenantID uuid.UUID) ([]byte, error) {
	if s.kms == nil {
		return nil, errors.New("acknowledgement: no KMS wired")
	}
	s.keyCacheMu.RLock()
	if k, ok := s.keyCache[tenantID]; ok {
		s.keyCacheMu.RUnlock()
		return k, nil
	}
	s.keyCacheMu.RUnlock()

	kekID := tenantKEKID(tenantID)
	var plaintext []byte
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		storedKEK, wrapped, getErr := s.repo.GetSigningKey(ctx, tx, tenantID)
		if getErr == nil {
			pt, err := s.kms.DecryptDataKey(ctx, storedKEK, wrapped)
			if err != nil {
				return fmt.Errorf("unwrap signing key: %w", err)
			}
			plaintext = pt
			return nil
		}
		// Not found → generate + persist.
		if vdmserr.KindOf(getErr) != vdmserr.KindNotFound {
			return getErr
		}
		pt, wrappedNew, err := s.kms.GenerateDataKey(ctx, kekID)
		if err != nil {
			return fmt.Errorf("generate signing key: %w", err)
		}
		if err := s.repo.InsertSigningKey(ctx, tx, tenantID, kekID, wrappedNew); err != nil {
			return err
		}
		plaintext = pt
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.keyCacheMu.Lock()
	s.keyCache[tenantID] = plaintext
	s.keyCacheMu.Unlock()
	return plaintext, nil
}

// VerifyAttestation recomputes the HMAC for a stored assignment and
// reports whether it matches. Exported for the CLI + integration
// tests; handlers use it only indirectly.
func (s *Service) VerifyAttestation(ctx context.Context, tenantID uuid.UUID, campaignID, userID uuid.UUID, at time.Time, stored []byte) (bool, error) {
	mac, err := s.attestationHMAC(ctx, tenantID, campaignID, userID, at)
	if err != nil {
		return false, err
	}
	return hmac.Equal(mac, stored), nil
}
