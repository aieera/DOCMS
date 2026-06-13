package crypto

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// KeyManager wraps a KMS / KEK provider. Implementations are responsible for
// generating plaintext DEKs, returning them alongside a wrapped form that can
// be persisted and later unwrapped.
type KeyManager interface {
	// GenerateDataKey returns a fresh plaintext DEK and its encrypted form
	// under the KEK identified by kekID.
	GenerateDataKey(ctx context.Context, kekID string) (plaintextDEK, encryptedDEK []byte, err error)

	// DecryptDataKey unwraps an encryptedDEK that was produced by
	// GenerateDataKey under the same kekID.
	DecryptDataKey(ctx context.Context, kekID string, encryptedDEK []byte) ([]byte, error)

	// EncryptDataKey wraps an EXISTING plaintext DEK under kekID, producing
	// the same wrapped form GenerateDataKey would. Unlike GenerateDataKey it
	// does not mint a new DEK — it is the primitive for re-wrapping an
	// existing DEK under a new KEK version (rotation re-wrap) without
	// regenerating it or touching the blob ciphertext. Callers should verify
	// round-trip (DecryptDataKey of the result equals the input) before
	// persisting, so a backend wrap bug fails closed instead of corrupting.
	EncryptDataKey(ctx context.Context, kekID string, plaintextDEK []byte) (encryptedDEK []byte, err error)

	// RotateKey re-wraps any data keys under a new KEK. Implementation is
	// backend-specific; for the Phase B skeleton it is a no-op.
	RotateKey(ctx context.Context, oldKEKID, newKEKID string) error
}

// ---- LocalKeyManager (dev / on-prem without cloud KMS) --------------------

// LocalKeyManager wraps DEKs with a PER-TENANT KEK derived from a master
// secret via HKDF-SHA256. See ADR 0022 for the rationale; see
// `docs/runbooks/06-key-management.md` for rotation + DR semantics.
//
// Derivation:
//
//	tenantKEK = HKDF-SHA256(
//	    ikm    = master_secret,
//	    salt   = "vaultdms/kek/" + kekID,
//	    info   = "vaultdms/kek/v1",
//	    length = 32 bytes,
//	)
//
// kekID comes from callers unchanged (e.g. "vaultdms/tenant/<uuid>" or
// "vaultdms/tenant/<uuid>@v2"). Two different kekIDs produce two
// independent 32-byte KEKs; knowing one reveals nothing about another.
//
// Not safe for production (master secret sits in process memory);
// Wave 6 follow-up swaps VaultKeyManager / AWSKMSKeyManager in for
// prod deployments.
//
// Wave 11.7: per-region masters. When kekID has the form
// "vaultdms/tenant/<uuid>/<region>" the manager picks
// regionMasters[region] before falling back to the global master.
// A US-region compromise therefore cannot decrypt EU-region
// ciphertexts. ADR 0026.
type LocalKeyManager struct {
	once   sync.Once
	master []byte
	// regionMasters maps region id → master secret. nil → single-
	// master behaviour, backwards-compatible with pre-11.7 kekIDs.
	regionMasters map[string][]byte
	warnFn        func(msg string)

	mu    sync.RWMutex
	cache map[string][]byte // kekID → derived tenant KEK (cached)
}

// NewLocalKeyManager constructs a LocalKeyManager from a base64 32-byte
// master secret. warnFn is called once on first use to log a prominent
// warning so it's obvious in prod logs if this implementation ever
// sneaks into a production deploy.
func NewLocalKeyManager(kekBase64 string, warnFn func(msg string)) (*LocalKeyManager, error) {
	if kekBase64 == "" {
		kekBase64 = os.Getenv("SEDOC_LOCAL_KEK")
	}
	if kekBase64 == "" {
		return nil, fmt.Errorf("LocalKeyManager: SEDOC_LOCAL_KEK not set")
	}
	master, err := base64.StdEncoding.DecodeString(kekBase64)
	if err != nil {
		return nil, fmt.Errorf("decode master: %w", err)
	}
	if len(master) != DEKSize {
		return nil, fmt.Errorf("master must be %d bytes, got %d", DEKSize, len(master))
	}
	if warnFn == nil {
		warnFn = func(string) {}
	}
	return &LocalKeyManager{
		master: master,
		warnFn: warnFn,
		cache:  make(map[string][]byte),
	}, nil
}

// NewMultiRegionLocalKeyManager constructs a LocalKeyManager with a
// default master plus per-region master secrets. Region ids follow
// upstream canonical form ("us-east-1", "eu-west-1", etc.). Empty
// values in regionalB64 are skipped so callers can populate only
// regions they operate in.
func NewMultiRegionLocalKeyManager(defaultB64 string, regionalB64 map[string]string, warnFn func(msg string)) (*LocalKeyManager, error) {
	base, err := NewLocalKeyManager(defaultB64, warnFn)
	if err != nil {
		return nil, err
	}
	base.regionMasters = make(map[string][]byte, len(regionalB64))
	for region, b64 := range regionalB64 {
		if b64 == "" {
			continue
		}
		m, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("region %q master decode: %w", region, err)
		}
		if len(m) != DEKSize {
			return nil, fmt.Errorf("region %q master must be %d bytes, got %d", region, DEKSize, len(m))
		}
		base.regionMasters[region] = m
	}
	return base, nil
}

// masterFor picks the master for a given kekID. Shape
// "vaultdms/tenant/<uuid>/<region>" (with optional "@v<N>" rotation
// suffix on the last segment) selects regionMasters[region];
// anything shorter falls back to the global master.
//
// A kekID claiming a region that isn't configured is a hard error
// — a silent fallback to the global master would break the
// per-region isolation promise.
func (l *LocalKeyManager) masterFor(kekID string) ([]byte, error) {
	if len(l.regionMasters) == 0 {
		return l.master, nil
	}
	trimmed := kekID
	if at := indexByte(trimmed, '@'); at > 0 {
		trimmed = trimmed[:at]
	}
	parts := splitOnSlash(trimmed)
	if len(parts) < 4 {
		return l.master, nil
	}
	region := parts[3]
	if m, ok := l.regionMasters[region]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("LocalKeyManager: region %q has no configured master (kekID=%s)", region, kekID)
}

// Local minimal helpers so the diff stays surgical and we don't
// pull in strings for two operations.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
func splitOnSlash(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func (l *LocalKeyManager) emitWarning() {
	l.once.Do(func() {
		l.warnFn("LocalKeyManager in use: suitable for dev/on-prem only. DO NOT USE IN SAAS PRODUCTION.")
	})
}

// deriveKEK computes (or returns cached) the per-tenant KEK for a
// given kekID. HKDF guarantees that knowing one tenant's KEK does
// not help recover another's (ADR 0022).
func (l *LocalKeyManager) deriveKEK(kekID string) ([]byte, error) {
	if kekID == "" {
		return nil, fmt.Errorf("LocalKeyManager: empty kekID — per-tenant derivation requires an alias")
	}
	l.mu.RLock()
	if cached, ok := l.cache[kekID]; ok {
		l.mu.RUnlock()
		return cached, nil
	}
	l.mu.RUnlock()

	master, err := l.masterFor(kekID)
	if err != nil {
		return nil, err
	}
	// HKDF(ikm=master, salt="vaultdms/kek/"+kekID, info="vaultdms/kek/v1")
	reader := hkdf.New(sha256.New, master, []byte("vaultdms/kek/"+kekID), []byte("vaultdms/kek/v1"))
	kek := make([]byte, DEKSize)
	if _, err := io.ReadFull(reader, kek); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}

	l.mu.Lock()
	l.cache[kekID] = kek
	l.mu.Unlock()
	return kek, nil
}

// GenerateDataKey returns a random DEK and the DEK encrypted under the
// per-tenant KEK derived from kekID.
func (l *LocalKeyManager) GenerateDataKey(_ context.Context, kekID string) ([]byte, []byte, error) {
	l.emitWarning()
	kek, err := l.deriveKEK(kekID)
	if err != nil {
		return nil, nil, err
	}
	dek, err := GenerateDEK()
	if err != nil {
		return nil, nil, err
	}
	ct, nonce, err := EncryptData(dek, kek)
	if err != nil {
		return nil, nil, err
	}
	wrapped := make([]byte, 0, len(nonce)+len(ct))
	wrapped = append(wrapped, nonce...)
	wrapped = append(wrapped, ct...)
	return dek, wrapped, nil
}

// EncryptDataKey wraps an existing DEK under the per-tenant KEK derived
// from kekID — identical to GenerateDataKey's wrap step but for a caller-
// supplied DEK. Output format (nonce||ciphertext) matches GenerateDataKey
// so DecryptDataKey unwraps it unchanged.
func (l *LocalKeyManager) EncryptDataKey(_ context.Context, kekID string, dek []byte) ([]byte, error) {
	l.emitWarning()
	if len(dek) != DEKSize {
		return nil, fmt.Errorf("EncryptDataKey: DEK must be %d bytes, got %d", DEKSize, len(dek))
	}
	kek, err := l.deriveKEK(kekID)
	if err != nil {
		return nil, err
	}
	ct, nonce, err := EncryptData(dek, kek)
	if err != nil {
		return nil, err
	}
	wrapped := make([]byte, 0, len(nonce)+len(ct))
	wrapped = append(wrapped, nonce...)
	wrapped = append(wrapped, ct...)
	return wrapped, nil
}

// DecryptDataKey unwraps a DEK previously produced by GenerateDataKey
// under the SAME kekID. Passing a different kekID fails (this is the
// cross-tenant isolation boundary on the local manager).
func (l *LocalKeyManager) DecryptDataKey(_ context.Context, kekID string, encryptedDEK []byte) ([]byte, error) {
	l.emitWarning()
	kek, err := l.deriveKEK(kekID)
	if err != nil {
		return nil, err
	}
	if len(encryptedDEK) < NonceSize+1 {
		return nil, fmt.Errorf("wrapped dek too short")
	}
	nonce := encryptedDEK[:NonceSize]
	ct := encryptedDEK[NonceSize:]
	return DecryptData(ct, nonce, kek)
}

// RotateKey for the local manager is a cache invalidation: since KEKs
// are derived from (master, kekID), "rotating" means minting a NEW
// kekID (e.g. adding a @v2 suffix) so the old kekID still decrypts
// historical data. Caller is responsible for updating the
// tenant_keks table and re-wrapping live DEKs via
// `dms-admin kms rotate --tenant <id>`.
func (l *LocalKeyManager) RotateKey(_ context.Context, oldKEKID, newKEKID string) error {
	if oldKEKID == "" || newKEKID == "" {
		return fmt.Errorf("RotateKey: old and new kekID required")
	}
	if oldKEKID == newKEKID {
		return fmt.Errorf("RotateKey: new kekID must differ from old")
	}
	// Warm the new KEK in the cache so the first write path doesn't
	// pay the HKDF cost under load.
	if _, err := l.deriveKEK(newKEKID); err != nil {
		return err
	}
	return nil
}

// ---- Vault Transit adapter (Wave 12.8) ------------------------------------
//
// VaultKeyManager wraps HashiCorp Vault's Transit engine. Transit
// mints and unwraps DEKs server-side — the plaintext DEK traverses
// the network on GenerateDataKey, but the master never leaves the
// Vault server. Network TLS (mTLS in prod) protects the DEK in
// flight; at rest the DEK lives only in the calling process for
// as long as encryptAll / decryptAll need it.
//
// The adapter delegates to a narrow `VaultTransitClient` interface
// so tests can mock the HTTP transport without pulling a real
// Vault into CI. Production wiring uses the official
// `github.com/hashicorp/vault/api` client.
type VaultKeyManager struct {
	client VaultTransitClient
	// KeyPrefix is prepended to the kekID when mapping to a
	// Vault key name, e.g. "vaultdms/" → Vault key
	// `vaultdms/vaultdms/tenant/<uuid>`. Operators configure it
	// so a shared Vault cluster can host multiple DMS instances.
	KeyPrefix string
}

// VaultTransitClient is the subset of the Vault Transit API the
// adapter consumes. Kept narrow so tests can stub without the
// full vault/api surface area.
type VaultTransitClient interface {
	// GenerateDataKey calls /transit/datakey/plaintext/<key>,
	// returning (plaintextDEK, wrappedDEK) for AES-256 (32-byte).
	GenerateDataKey(ctx context.Context, key string) (plaintext, wrapped []byte, err error)
	// Encrypt calls /transit/encrypt/<key>, wrapping a caller-supplied
	// plaintext (an existing DEK) and returning the Vault ciphertext —
	// the same wrapped form GenerateDataKey emits, so Decrypt unwraps it.
	Encrypt(ctx context.Context, key string, plaintext []byte) (wrapped []byte, err error)
	// Decrypt calls /transit/decrypt/<key>, returning the plaintext DEK.
	Decrypt(ctx context.Context, key string, wrapped []byte) ([]byte, error)
	// Rotate calls /transit/keys/<key>/rotate. Vault preserves the
	// old key version so pre-rotation ciphertexts still decrypt.
	Rotate(ctx context.Context, key string) error
}

// NewVaultKeyManager constructs a Vault-backed KeyManager.
func NewVaultKeyManager(client VaultTransitClient, keyPrefix string) (*VaultKeyManager, error) {
	if client == nil {
		return nil, fmt.Errorf("VaultKeyManager: client required")
	}
	return &VaultKeyManager{client: client, KeyPrefix: keyPrefix}, nil
}

// GenerateDataKey proxies to Vault's transit/datakey/plaintext.
func (v *VaultKeyManager) GenerateDataKey(ctx context.Context, kekID string) (plaintext, wrapped []byte, err error) {
	if v.client == nil {
		return nil, nil, ErrKMSNotConfigured
	}
	return v.client.GenerateDataKey(ctx, v.KeyPrefix+kekID)
}

// EncryptDataKey proxies to Vault's transit/encrypt to wrap an existing DEK.
func (v *VaultKeyManager) EncryptDataKey(ctx context.Context, kekID string, plaintextDEK []byte) ([]byte, error) {
	if v.client == nil {
		return nil, ErrKMSNotConfigured
	}
	return v.client.Encrypt(ctx, v.KeyPrefix+kekID, plaintextDEK)
}

// DecryptDataKey proxies to Vault's transit/decrypt.
func (v *VaultKeyManager) DecryptDataKey(ctx context.Context, kekID string, wrapped []byte) ([]byte, error) {
	if v.client == nil {
		return nil, ErrKMSNotConfigured
	}
	return v.client.Decrypt(ctx, v.KeyPrefix+kekID, wrapped)
}

// RotateKey delegates to Transit's versioned key rotation. Old
// versions keep decrypting historical ciphertexts.
func (v *VaultKeyManager) RotateKey(ctx context.Context, oldKEKID, _ string) error {
	if v.client == nil {
		return ErrKMSNotConfigured
	}
	// Vault Transit rotates in place; the "new" kekID arg is
	// ignored because Vault tracks versions internally. Callers
	// that want a new alias can rename via the usual @v<N> suffix.
	return v.client.Rotate(ctx, v.KeyPrefix+oldKEKID)
}

// ---- AWS KMS adapter (Wave 12.8) -------------------------------------------
//
// AWSKMSKeyManager wraps the AWS KMS GenerateDataKey + Decrypt
// APIs. kekID maps to a KMS key alias (e.g. `alias/vaultdms-<uuid>`);
// operators must have provisioned the alias beforehand via
// Terraform / CloudFormation.
type AWSKMSKeyManager struct {
	client AWSKMSClient
	// AliasPrefix is prepended to the kekID when mapping to a KMS
	// alias, e.g. "alias/vaultdms-".
	AliasPrefix string
}

// AWSKMSClient is the narrow subset the adapter needs. Production
// wiring uses the official aws-sdk-go-v2 KMS client.
type AWSKMSClient interface {
	// GenerateDataKey calls kms:GenerateDataKey with KeySpec=AES_256.
	GenerateDataKey(ctx context.Context, keyID string) (plaintext, wrapped []byte, err error)
	// Encrypt calls kms:Encrypt to wrap a caller-supplied plaintext (an
	// existing DEK), returning the ciphertext blob — the same wrapped form
	// GenerateDataKey emits, so Decrypt unwraps it.
	Encrypt(ctx context.Context, keyID string, plaintext []byte) (wrapped []byte, err error)
	// Decrypt calls kms:Decrypt. AWS KMS self-identifies the key
	// from the ciphertext blob, so the keyID is only a consistency
	// check (and trust boundary).
	Decrypt(ctx context.Context, keyID string, wrapped []byte) ([]byte, error)
	// ScheduleKeyDeletion backs the Wave 12.7b reaper. Window is
	// in days (AWS min 7, max 30).
	ScheduleKeyDeletion(ctx context.Context, keyID string, windowDays int32) error
}

// NewAWSKMSKeyManager constructs an AWS KMS-backed KeyManager.
func NewAWSKMSKeyManager(client AWSKMSClient, aliasPrefix string) (*AWSKMSKeyManager, error) {
	if client == nil {
		return nil, fmt.Errorf("AWSKMSKeyManager: client required")
	}
	if aliasPrefix == "" {
		aliasPrefix = "alias/vaultdms-"
	}
	return &AWSKMSKeyManager{client: client, AliasPrefix: aliasPrefix}, nil
}

// keyID maps a kekID to the AWS KMS alias form. We normalise
// slashes to dashes because AWS alias names allow alphanumeric +
// [_ -] but not `/`; so "vaultdms/tenant/<uuid>/<region>" becomes
// "alias/vaultdms-vaultdms-tenant-<uuid>-<region>".
func (a *AWSKMSKeyManager) keyID(kekID string) string {
	safe := make([]byte, 0, len(kekID))
	for i := 0; i < len(kekID); i++ {
		c := kekID[i]
		if c == '/' || c == '@' {
			safe = append(safe, '-')
			continue
		}
		safe = append(safe, c)
	}
	return a.AliasPrefix + string(safe)
}

// GenerateDataKey proxies to kms:GenerateDataKey.
func (a *AWSKMSKeyManager) GenerateDataKey(ctx context.Context, kekID string) (plaintext, wrapped []byte, err error) {
	if a.client == nil {
		return nil, nil, ErrKMSNotConfigured
	}
	return a.client.GenerateDataKey(ctx, a.keyID(kekID))
}

// EncryptDataKey proxies to kms:Encrypt to wrap an existing DEK.
func (a *AWSKMSKeyManager) EncryptDataKey(ctx context.Context, kekID string, plaintextDEK []byte) ([]byte, error) {
	if a.client == nil {
		return nil, ErrKMSNotConfigured
	}
	return a.client.Encrypt(ctx, a.keyID(kekID), plaintextDEK)
}

// DecryptDataKey proxies to kms:Decrypt.
func (a *AWSKMSKeyManager) DecryptDataKey(ctx context.Context, kekID string, wrapped []byte) ([]byte, error) {
	if a.client == nil {
		return nil, ErrKMSNotConfigured
	}
	return a.client.Decrypt(ctx, a.keyID(kekID), wrapped)
}

// RotateKey — AWS KMS has no in-place rotation for CMKs; operators
// provision a new alias (e.g. `alias/vaultdms-<uuid>-v2`). The
// adapter's RotateKey records the intent; actual alias provisioning
// is a Terraform / admin-SDK action outside the hot path.
func (a *AWSKMSKeyManager) RotateKey(_ context.Context, _, _ string) error {
	return fmt.Errorf("AWSKMSKeyManager: CMK rotation happens via IaC alias provisioning; see runbooks/06-key-management.md")
}

// ErrKMSNotConfigured is returned when an adapter is constructed
// without a client — mostly relevant in tests.
var ErrKMSNotConfigured = fmt.Errorf("KeyManager: client not configured")
