package crypto

import (
	"bytes"
	"context"
	"testing"
)

// The external-KMS admin API (§5/§8) models KEK revocation / rotation by moving
// the tenant to a new kekID (version alias). These tests pin the load-bearing
// property the "revoking access is testable" DoD relies on: a DEK wrapped under
// one kekID cannot be unwrapped under any other kekID — so once a tenant's live
// KEK version is revoked/rotated, ciphertext bound to the old version is
// inaccessible via the new one. LocalKeyManager derives a distinct per-kekID KEK
// (HKDF over kekID), which makes this a pure fails-closed unit test with no
// external KMS.
func TestLocalKeyManager_UnwrapUnderRevokedKekFailsClosed(t *testing.T) {
	km, err := NewLocalKeyManager(newB64Secret(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const live = "vaultdms/tenant/00000000-0000-0000-0000-0000000000ab@v1"
	// v2 stands in for the post-rotation / post-revocation live version.
	const rotated = "vaultdms/tenant/00000000-0000-0000-0000-0000000000ab@v2"

	dek, wrapped, err := km.GenerateDataKey(ctx, live)
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}

	// Sanity: the wrap round-trips under its own version.
	got, err := km.DecryptDataKey(ctx, live, wrapped)
	if err != nil {
		t.Fatalf("DecryptDataKey under live version: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("round-trip DEK mismatch under live version")
	}

	// Revoke/rotate: the same ciphertext must NOT unwrap under the new version.
	if _, err := km.DecryptDataKey(ctx, rotated, wrapped); err == nil {
		t.Fatal("expected unwrap under rotated/revoked kekID to fail closed, got nil error")
	}
}

// A completely unrelated kekID must likewise never unwrap another tenant/
// version's DEK — the isolation guarantee under revocation.
func TestLocalKeyManager_UnwrapUnderForeignKekFailsClosed(t *testing.T) {
	km, err := NewLocalKeyManager(newB64Secret(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, wrapped, err := km.GenerateDataKey(ctx, "vaultdms/tenant/aaaa@v1")
	if err != nil {
		t.Fatalf("GenerateDataKey: %v", err)
	}
	for i, foreign := range []string{
		"vaultdms/tenant/bbbb@v1",
		"vaultdms/tenant/aaaa@v9",
		"vaultdms/kek/aaaa",
	} {
		if _, err := km.DecryptDataKey(ctx, foreign, wrapped); err == nil {
			t.Fatalf("case %d: unwrap under foreign kekID %q should fail closed", i, foreign)
		}
	}
}
