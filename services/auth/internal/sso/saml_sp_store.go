package sso

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/crypto"
)

// The SAML SP identity is a single service-wide keypair persisted in
// saml_sp_keypair (auth migration 000002). The private key is sealed
// under the deployment KEK; the certificate is public. Generated once on
// first boot and reused across restarts so the SP cert an IdP pins stays
// stable — the fix for restart-induced SSO outages.

// ResolveSPKeyMaterial is what main wires. Precedence:
//  1. operator-injected PEMs (SEDOC_SAML_SP_KEY_PEM / _CERT_PEM — a
//     K8s secret or Vault-agent file) → LoadSPKeyMaterial, no DB write;
//  2. otherwise the durable app-managed keypair in Postgres, generated
//     once on first boot (LoadOrCreateSPKeyMaterial).
//
// Either way the resolved cert fingerprint is logged at startup so an
// operator can confirm it is stable across restarts.
func ResolveSPKeyMaterial(ctx context.Context, pool *pgxpool.Pool, kek []byte, keyPEM, certPEM string, log zerolog.Logger) (*SPKeyMaterial, error) {
	if strings.TrimSpace(keyPEM) != "" && strings.TrimSpace(certPEM) != "" {
		m, err := LoadSPKeyMaterial([]byte(keyPEM), []byte(certPEM))
		if err != nil {
			return nil, fmt.Errorf("load injected SP key material: %w", err)
		}
		log.Info().Str("source", "env").Str("sp_cert_fingerprint", CertFingerprint(m.Certificate)).
			Msg("SAML SP identity loaded from injected PEMs")
		return m, nil
	}
	// Without a KEK we can't seal the private key for durable storage.
	// Rather than persist an unsealed key, fall back to an EPHEMERAL
	// self-signed cert — dev-only degraded mode. Warn loudly: the cert
	// changes on restart, so an IdP must not pin it here.
	if len(kek) != crypto.DEKSize {
		m, err := NewSelfSignedSP()
		if err != nil {
			return nil, err
		}
		log.Warn().Str("sp_cert_fingerprint", CertFingerprint(m.Certificate)).
			Msg("SAML SP identity is EPHEMERAL (no KEK to seal a durable key) — it CHANGES on restart; set SEDOC_LOCAL_KEK or inject SEDOC_SAML_SP_{KEY,CERT}_PEM for a stable pinnable cert")
		return m, nil
	}
	m, created, err := LoadOrCreateSPKeyMaterial(ctx, pool, kek)
	if err != nil {
		return nil, err
	}
	src := "postgres"
	if created {
		src = "postgres (generated on first boot)"
	}
	log.Info().Str("source", src).Str("sp_cert_fingerprint", CertFingerprint(m.Certificate)).
		Msg("SAML SP identity resolved")
	return m, nil
}

// LoadOrCreateSPKeyMaterial returns the durable SP keypair, generating +
// persisting it on first boot. Concurrent replica boots race safely: the
// INSERT uses ON CONFLICT DO NOTHING and the loser re-reads the winner's
// row, so every replica ends up with the SAME keypair. `created` reports
// whether this call generated it.
func LoadOrCreateSPKeyMaterial(ctx context.Context, pool *pgxpool.Pool, kek []byte) (m *SPKeyMaterial, created bool, err error) {
	if pool == nil {
		return nil, false, errors.New("saml sp store: nil pool")
	}
	if m, err = loadSPRow(ctx, pool, kek); err == nil {
		return m, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	// Absent → generate + persist. ON CONFLICT DO NOTHING makes a
	// concurrent boot's insert a no-op; we then re-read whichever row won.
	fresh, err := NewSelfSignedSP()
	if err != nil {
		return nil, false, err
	}
	sealedKey, err := sealSPKey(fresh.PrivateKey, kek)
	if err != nil {
		return nil, false, err
	}
	certPEM := encodeCertPEM(fresh.Certificate)
	fp := CertFingerprint(fresh.Certificate)
	ct, err := pool.Exec(ctx, `
		INSERT INTO saml_sp_keypair (id, private_key_sealed, certificate_pem, fingerprint)
		VALUES (TRUE, $1, $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		sealedKey, certPEM, fp)
	if err != nil {
		return nil, false, fmt.Errorf("persist sp keypair: %w", err)
	}
	// Always re-read: on a race we want the winner's row, not `fresh`.
	m, err = loadSPRow(ctx, pool, kek)
	if err != nil {
		return nil, false, err
	}
	return m, ct.RowsAffected() == 1, nil
}

// RotateSPKeyMaterial generates a NEW keypair and replaces the singleton
// row (bumping rotated_at) — the controlled, operator-driven change.
// Returns the new material; the new fingerprint differs from the old, so
// IdP admins must re-pin (see the rotation runbook).
func RotateSPKeyMaterial(ctx context.Context, pool *pgxpool.Pool, kek []byte) (*SPKeyMaterial, error) {
	if pool == nil {
		return nil, errors.New("saml sp store: nil pool")
	}
	fresh, err := NewSelfSignedSP()
	if err != nil {
		return nil, err
	}
	sealedKey, err := sealSPKey(fresh.PrivateKey, kek)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO saml_sp_keypair (id, private_key_sealed, certificate_pem, fingerprint, rotated_at)
		VALUES (TRUE, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE
		   SET private_key_sealed = EXCLUDED.private_key_sealed,
		       certificate_pem    = EXCLUDED.certificate_pem,
		       fingerprint        = EXCLUDED.fingerprint,
		       rotated_at         = now()`,
		sealedKey, encodeCertPEM(fresh.Certificate), CertFingerprint(fresh.Certificate))
	if err != nil {
		return nil, fmt.Errorf("rotate sp keypair: %w", err)
	}
	return loadSPRow(ctx, pool, kek)
}

// loadSPRow reads + unseals the persisted keypair. Returns pgx.ErrNoRows
// when the row doesn't exist yet.
func loadSPRow(ctx context.Context, pool *pgxpool.Pool, kek []byte) (*SPKeyMaterial, error) {
	var sealedKey []byte
	var certPEM string
	if err := pool.QueryRow(ctx,
		`SELECT private_key_sealed, certificate_pem FROM saml_sp_keypair WHERE id = TRUE`,
	).Scan(&sealedKey, &certPEM); err != nil {
		return nil, err
	}
	key, err := unsealSPKey(sealedKey, kek)
	if err != nil {
		return nil, fmt.Errorf("unseal sp key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return LoadSPKeyMaterial(keyPEM, []byte(certPEM))
}

// CertFingerprint is the SHA-256 of the certificate DER, hex, colon-
// separated and upper-cased — the form IdP admin UIs display for pinning.
func CertFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func encodeCertPEM(cert *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

// sealSPKey wraps the PKCS#1 DER of the private key as nonce||ciphertext
// under the KEK (same envelope shape as the LDAP bind-password seal).
func sealSPKey(key *rsa.PrivateKey, kek []byte) ([]byte, error) {
	if len(kek) != crypto.DEKSize {
		return nil, fmt.Errorf("saml sp key seal: KEK must be %d bytes, got %d", crypto.DEKSize, len(kek))
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	ct, nonce, err := crypto.EncryptData(der, kek)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, nonce...), ct...), nil
}

// unsealSPKey reverses sealSPKey.
func unsealSPKey(sealed, kek []byte) (*rsa.PrivateKey, error) {
	if len(kek) != crypto.DEKSize {
		return nil, fmt.Errorf("saml sp key unseal: KEK must be %d bytes, got %d", crypto.DEKSize, len(kek))
	}
	if len(sealed) < crypto.NonceSize {
		return nil, errors.New("saml sp key unseal: sealed blob too short")
	}
	nonce, ct := sealed[:crypto.NonceSize], sealed[crypto.NonceSize:]
	der, err := crypto.DecryptData(ct, nonce, kek)
	if err != nil {
		return nil, err
	}
	return x509.ParsePKCS1PrivateKey(der)
}
