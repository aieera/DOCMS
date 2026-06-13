package crypto

// Wave 12.8 — adapter contract tests. No real Vault or AWS call;
// we stub the narrow interfaces to verify the adapter layer's
// routing + prefixing logic.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubVault records the calls so assertions can examine them.
type stubVault struct {
	genKey, encKey, decKey, rotKey string
}

func (s *stubVault) GenerateDataKey(_ context.Context, key string) ([]byte, []byte, error) {
	s.genKey = key
	return []byte("pt012345678901234567890123456789"), []byte("wrapped"), nil
}
func (s *stubVault) Encrypt(_ context.Context, key string, _ []byte) ([]byte, error) {
	s.encKey = key
	return []byte("wrapped"), nil
}
func (s *stubVault) Decrypt(_ context.Context, key string, _ []byte) ([]byte, error) {
	s.decKey = key
	return []byte("pt012345678901234567890123456789"), nil
}
func (s *stubVault) Rotate(_ context.Context, key string) error {
	s.rotKey = key
	return nil
}

func TestVaultKeyManager_PrefixesKekID(t *testing.T) {
	s := &stubVault{}
	km, err := NewVaultKeyManager(s, "dms-prod/")
	require.NoError(t, err)

	_, _, err = km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc")
	require.NoError(t, err)
	require.Equal(t, "dms-prod/vaultdms/tenant/abc", s.genKey)

	_, err = km.DecryptDataKey(context.Background(), "vaultdms/tenant/abc", []byte("wrapped"))
	require.NoError(t, err)
	require.Equal(t, "dms-prod/vaultdms/tenant/abc", s.decKey)

	require.NoError(t, km.RotateKey(context.Background(), "vaultdms/tenant/abc", ""))
	require.Equal(t, "dms-prod/vaultdms/tenant/abc", s.rotKey)
}

func TestVaultKeyManager_NilClientRejected(t *testing.T) {
	_, err := NewVaultKeyManager(nil, "")
	require.Error(t, err)
}

// stubAWSKMS records the translated key id so we can verify the
// slash → dash normalisation.
type stubAWSKMS struct {
	genKey, encKey, decKey string
}

func (s *stubAWSKMS) GenerateDataKey(_ context.Context, keyID string) ([]byte, []byte, error) {
	s.genKey = keyID
	return []byte("pt012345678901234567890123456789"), []byte("wrapped"), nil
}
func (s *stubAWSKMS) Encrypt(_ context.Context, keyID string, _ []byte) ([]byte, error) {
	s.encKey = keyID
	return []byte("wrapped"), nil
}
func (s *stubAWSKMS) Decrypt(_ context.Context, keyID string, _ []byte) ([]byte, error) {
	s.decKey = keyID
	return []byte("pt012345678901234567890123456789"), nil
}
func (s *stubAWSKMS) ScheduleKeyDeletion(_ context.Context, _ string, _ int32) error {
	return nil
}

func TestAWSKMSKeyManager_NormalisesSlashesToDashes(t *testing.T) {
	// AWS KMS aliases can't contain '/' or '@'. The adapter flips
	// both to '-' so a kekID like
	// "vaultdms/tenant/<uuid>/eu-west-1@v2" becomes a valid alias.
	s := &stubAWSKMS{}
	km, err := NewAWSKMSKeyManager(s, "alias/vdms-")
	require.NoError(t, err)

	_, _, err = km.GenerateDataKey(context.Background(), "vaultdms/tenant/abc/eu-west-1@v2")
	require.NoError(t, err)
	require.Equal(t, "alias/vdms-vaultdms-tenant-abc-eu-west-1-v2", s.genKey)
}

func TestAWSKMSKeyManager_DefaultAliasPrefix(t *testing.T) {
	s := &stubAWSKMS{}
	km, err := NewAWSKMSKeyManager(s, "")
	require.NoError(t, err)
	_, _, err = km.GenerateDataKey(context.Background(), "k")
	require.NoError(t, err)
	require.Equal(t, "alias/vaultdms-k", s.genKey)
}

func TestAWSKMSKeyManager_RotateErrors(t *testing.T) {
	// AWS CMKs rotate via IaC alias provisioning; the adapter
	// surfaces a typed error so operators don't call it at runtime.
	s := &stubAWSKMS{}
	km, err := NewAWSKMSKeyManager(s, "")
	require.NoError(t, err)
	require.Error(t, km.RotateKey(context.Background(), "old", "new"))
}
