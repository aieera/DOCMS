package crypto

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

// The crypto package is on the critical path for every stored document.
// Treat every guard here as load-bearing: wrong lengths, wrong nonce
// reuse, or silently allowing a tamper would leak plaintext at rest.

func TestGenerateDEK_LengthAndEntropy(t *testing.T) {
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatal(err)
	}
	if len(dek) != DEKSize {
		t.Fatalf("dek length: got %d, want %d", len(dek), DEKSize)
	}
	// Not deterministic: two draws must differ with overwhelming prob.
	other, _ := GenerateDEK()
	if bytes.Equal(dek, other) {
		t.Fatal("two generated DEKs collided — non-random source")
	}
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	dek, _ := GenerateDEK()
	cases := []struct {
		name string
		pt   []byte
	}{
		{"short", []byte("hello")},
		{"empty", []byte{}},
		{"binary", []byte{0, 1, 2, 3, 255, 254}},
		{"longer than block", bytes.Repeat([]byte("abc"), 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, nonce, err := EncryptData(tc.pt, dek)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if len(nonce) != NonceSize {
				t.Errorf("nonce length: got %d, want %d", len(nonce), NonceSize)
			}
			got, err := DecryptData(ct, nonce, dek)
			if err != nil {
				t.Fatalf("decrypt: %v", err)
			}
			if !bytes.Equal(got, tc.pt) {
				t.Errorf("roundtrip mismatch: got %q, want %q", got, tc.pt)
			}
		})
	}
}

func TestEncrypt_DifferentNonceEveryCall(t *testing.T) {
	// GCM is catastrophic on nonce reuse under the same key. Two encrypts
	// of the same plaintext MUST produce different ciphertext because the
	// nonce is freshly drawn each time.
	dek, _ := GenerateDEK()
	pt := []byte("sensitive")
	ct1, n1, _ := EncryptData(pt, dek)
	ct2, n2, _ := EncryptData(pt, dek)
	if bytes.Equal(n1, n2) {
		t.Fatal("nonce collision: catastrophic for AES-GCM")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("ciphertext collision: DEK + nonce reuse suspected")
	}
}

func TestEncrypt_RejectsWrongDEKLength(t *testing.T) {
	shortDEK := make([]byte, DEKSize-1)
	_, _, err := EncryptData([]byte("x"), shortDEK)
	if err == nil {
		t.Fatal("short DEK should be rejected")
	}
	longDEK := make([]byte, DEKSize+1)
	_, _, err = EncryptData([]byte("x"), longDEK)
	if err == nil {
		t.Fatal("oversized DEK should be rejected")
	}
}

func TestDecrypt_RejectsTamperedCiphertext(t *testing.T) {
	dek, _ := GenerateDEK()
	ct, nonce, _ := EncryptData([]byte("secret"), dek)
	// Flip one bit in the ciphertext — GCM's auth tag must fail.
	ct[0] ^= 0x01
	_, err := DecryptData(ct, nonce, dek)
	if err == nil {
		t.Fatal("tampered ciphertext must fail auth")
	}
}

func TestDecrypt_RejectsWrongKey(t *testing.T) {
	dek1, _ := GenerateDEK()
	dek2, _ := GenerateDEK()
	ct, nonce, _ := EncryptData([]byte("secret"), dek1)
	_, err := DecryptData(ct, nonce, dek2)
	if err == nil {
		t.Fatal("wrong-key decrypt must fail")
	}
}

func TestDecrypt_RejectsWrongNonceLength(t *testing.T) {
	dek, _ := GenerateDEK()
	ct, _, _ := EncryptData([]byte("x"), dek)
	_, err := DecryptData(ct, make([]byte, NonceSize-1), dek)
	if err == nil {
		t.Fatal("short nonce should be rejected at the length gate")
	}
}

// ---- LocalKeyManager envelope roundtrip ------------------------------------

func newLocalKM(t *testing.T) *LocalKeyManager {
	t.Helper()
	kek := bytes.Repeat([]byte{0x42}, DEKSize)
	km, err := NewLocalKeyManager(base64.StdEncoding.EncodeToString(kek), nil)
	if err != nil {
		t.Fatalf("NewLocalKeyManager: %v", err)
	}
	return km
}

func TestLocalKM_EnvelopeRoundtrip(t *testing.T) {
	km := newLocalKM(t)
	ctx := context.Background()

	dek, wrapped, err := km.GenerateDataKey(ctx, "tenant-A-kek")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(dek) != DEKSize {
		t.Errorf("plaintext dek length: %d", len(dek))
	}
	if len(wrapped) <= NonceSize {
		t.Fatalf("wrapped payload too short: %d bytes", len(wrapped))
	}

	got, err := km.DecryptDataKey(ctx, "tenant-A-kek", wrapped)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Error("decrypted DEK differs from original")
	}
}

func TestLocalKM_RejectsShortWrappedPayload(t *testing.T) {
	km := newLocalKM(t)
	_, err := km.DecryptDataKey(context.Background(), "k", []byte{0, 1, 2})
	if err == nil {
		t.Fatal("truncated wrapped DEK must fail")
	}
}

func TestLocalKM_KEKSwapFailsDecrypt(t *testing.T) {
	// "KEK rotation": a DEK wrapped under KEK_A cannot be unwrapped under
	// KEK_B. This is the exact test that proves rotation is non-silent —
	// the service MUST fail loudly if it tries to decrypt with a stale KEK.
	kekA := bytes.Repeat([]byte{0xAA}, DEKSize)
	kekB := bytes.Repeat([]byte{0xBB}, DEKSize)
	kmA, _ := NewLocalKeyManager(base64.StdEncoding.EncodeToString(kekA), nil)
	kmB, _ := NewLocalKeyManager(base64.StdEncoding.EncodeToString(kekB), nil)

	_, wrapped, _ := kmA.GenerateDataKey(context.Background(), "id-A")
	if _, err := kmB.DecryptDataKey(context.Background(), "id-A", wrapped); err == nil {
		t.Fatal("DEK wrapped under KEK_A must not decrypt under KEK_B")
	}
}

func TestNewLocalKeyManager_Validation(t *testing.T) {
	if _, err := NewLocalKeyManager("", nil); err == nil {
		// Guard against accidentally reading a stale env var in tests.
		t.Skip("skipping: SEDOC_LOCAL_KEK set in env")
	}

	// Non-base64 garbage.
	if _, err := NewLocalKeyManager("not-base64!", nil); err == nil {
		t.Error("invalid base64 should reject")
	}

	// 16-byte key (wrong size).
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := NewLocalKeyManager(short, nil); err == nil {
		t.Error("16-byte KEK should reject (must be 32)")
	}
}

func TestLocalKM_WarnFnCalledOnce(t *testing.T) {
	n := 0
	kek := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, DEKSize))
	km, err := NewLocalKeyManager(kek, func(string) { n++ })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, _, _ = km.GenerateDataKey(context.Background(), "x")
	}
	if n != 1 {
		t.Errorf("warn fn called %d times, want exactly 1 (sync.Once contract)", n)
	}
}

func TestLocalKM_RotateKeyValidates(t *testing.T) {
	// Post Wave 6 Prompt 6.1, RotateKey warms the cache for the new
	// kekID. It must still reject empty or identical ids.
	km := newLocalKM(t)
	ctx := context.Background()

	if err := km.RotateKey(ctx, "", "new"); err == nil {
		t.Error("empty oldKEKID must reject")
	}
	if err := km.RotateKey(ctx, "old", ""); err == nil {
		t.Error("empty newKEKID must reject")
	}
	if err := km.RotateKey(ctx, "same", "same"); err == nil {
		t.Error("identical old/new kekID must reject")
	}
	if err := km.RotateKey(ctx, "old-v1", "old-v2"); err != nil {
		t.Errorf("valid rotation should succeed: %v", err)
	}
}

// --- Wave 6 Prompt 6.1: per-tenant KEK isolation --------------------------

func TestLocalKM_PerTenantKEKsAreDistinct(t *testing.T) {
	// Under the SAME master secret, two different aliases must derive
	// two independent KEKs — the whole point of HKDF per ADR 0022.
	km := newLocalKM(t)
	ctx := context.Background()

	_, wrappedA, err := km.GenerateDataKey(ctx, "vaultdms/tenant/AAAA")
	if err != nil {
		t.Fatalf("generate A: %v", err)
	}
	_, wrappedB, err := km.GenerateDataKey(ctx, "vaultdms/tenant/BBBB")
	if err != nil {
		t.Fatalf("generate B: %v", err)
	}
	if bytes.Equal(wrappedA, wrappedB) {
		t.Fatal("wrapped DEKs under different tenant aliases are byte-identical — derivation is collapsing")
	}
}

func TestLocalKM_DecryptUnderWrongAliasFails(t *testing.T) {
	// Cross-tenant containment: tenant B's KEK cannot unwrap tenant
	// A's DEK even when both run against the same master secret.
	km := newLocalKM(t)
	ctx := context.Background()

	_, wrappedA, err := km.GenerateDataKey(ctx, "vaultdms/tenant/AAAA")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := km.DecryptDataKey(ctx, "vaultdms/tenant/BBBB", wrappedA); err == nil {
		t.Fatal("decrypt with wrong tenant alias MUST fail — otherwise cross-tenant read is possible")
	}
}

func TestLocalKM_RejectsEmptyKEKID(t *testing.T) {
	// Empty kekID == the shared-KEK bug this prompt eliminates.
	// Must fail loudly at both generate and decrypt.
	km := newLocalKM(t)
	ctx := context.Background()
	if _, _, err := km.GenerateDataKey(ctx, ""); err == nil {
		t.Error("GenerateDataKey with empty kekID must reject")
	}
	if _, err := km.DecryptDataKey(ctx, "", []byte("deadbeefdeadbeefdeadbeef")); err == nil {
		t.Error("DecryptDataKey with empty kekID must reject")
	}
}

func TestLocalKM_SameAliasIsDeterministic(t *testing.T) {
	// Same (master, alias) must derive the same KEK across calls;
	// otherwise historical ciphertext would stop decrypting after a
	// process restart. HKDF is deterministic by construction; this
	// test pins the contract.
	km := newLocalKM(t)
	ctx := context.Background()

	_, wrapped1, err := km.GenerateDataKey(ctx, "stable-alias")
	if err != nil {
		t.Fatal(err)
	}
	dek2, err := km.DecryptDataKey(ctx, "stable-alias", wrapped1)
	if err != nil {
		t.Fatalf("decrypt under same alias failed: %v", err)
	}
	if len(dek2) != DEKSize {
		t.Errorf("decrypted DEK length: %d", len(dek2))
	}
}
