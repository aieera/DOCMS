package crypto

import (
	"bytes"
	"context"
	"testing"
)

// TestLocalKM_RewrapDEK_RoundTrip is the safety crux of rotation re-wrap:
// a DEK wrapped under alias v1, then re-wrapped (via EncryptDataKey) under
// alias v2, must still decrypt to the SAME DEK under v2 — and the original
// v1 wrapping must keep working. This is exactly the invariant the storage
// RewrapDEK primitive verifies before touching the DB.
func TestLocalKM_RewrapDEK_RoundTrip(t *testing.T) {
	km := newLocalKM(t)
	ctx := context.Background()
	const (
		aliasV1 = "vaultdms/tenant/abc"
		aliasV2 = "vaultdms/tenant/abc@v2"
	)

	dek, encV1, err := km.GenerateDataKey(ctx, aliasV1)
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}

	// Re-wrap the SAME DEK under v2.
	encV2, err := km.EncryptDataKey(ctx, aliasV2, dek)
	if err != nil {
		t.Fatalf("EncryptDataKey: %v", err)
	}

	// The v2 wrapping must unwrap back to the identical DEK.
	got, err := km.DecryptDataKey(ctx, aliasV2, encV2)
	if err != nil {
		t.Fatalf("DecryptDataKey(v2): %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("v2 unwrap mismatch: re-wrap did not preserve the DEK")
	}

	// The original v1 wrapping must still work (historical ciphertext).
	gotV1, err := km.DecryptDataKey(ctx, aliasV1, encV1)
	if err != nil {
		t.Fatalf("DecryptDataKey(v1): %v", err)
	}
	if !bytes.Equal(gotV1, dek) {
		t.Fatalf("v1 unwrap mismatch after re-wrap")
	}

	// Cross-alias isolation: v2 ciphertext must NOT unwrap under v1.
	if _, err := km.DecryptDataKey(ctx, aliasV1, encV2); err == nil {
		t.Fatalf("expected v1 to reject v2-wrapped DEK (KEK isolation broken)")
	}
}

// TestLocalKM_EncryptDataKey_RejectsWrongSize guards the DEK-size check.
func TestLocalKM_EncryptDataKey_RejectsWrongSize(t *testing.T) {
	km := newLocalKM(t)
	if _, err := km.EncryptDataKey(context.Background(), "vaultdms/tenant/abc", []byte("short")); err == nil {
		t.Fatalf("expected error for non-32-byte DEK")
	}
}
