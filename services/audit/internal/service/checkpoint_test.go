package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"testing"

	"github.com/rs/zerolog"
)

func newSeedB64() string {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(seed)
}

// TestSigningKey_ExternalVerifiability is the load-bearing guarantee of
// the signed-checkpoint feature: a third party holding ONLY the published
// public key + the canonical message format can verify a checkpoint
// signature, and any alteration of the signed fields fails verification.
func TestSigningKey_ExternalVerifiability(t *testing.T) {
	svc := New(Config{SigningKeySeedB64: newSeedB64(), Logger: zerolog.Nop()})

	info := svc.SigningKey()
	if !info.Enabled || info.Algo != "ed25519" || info.KeyID == "" || info.PublicKey == "" {
		t.Fatalf("unexpected signing key info: %+v", info)
	}
	pubBytes, err := base64.StdEncoding.DecodeString(info.PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		t.Fatalf("public key not decodable to ed25519 key: %v len=%d", err, len(pubBytes))
	}
	pub := ed25519.PublicKey(pubBytes)

	// The server signs with the private key; reproduce that here (the same
	// seed) to obtain a signature an external verifier would receive.
	priv := svc.signer
	msg := CanonicalCheckpointMessage("tenant-1", "abc123head", 42)
	sig := ed25519.Sign(priv, msg)

	// External verifier: only pub + canonical message.
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatal("external verification of a valid checkpoint failed")
	}
	// Tamper: altering any signed field must break verification.
	if ed25519.Verify(pub, CanonicalCheckpointMessage("tenant-1", "abc123head", 43), sig) {
		t.Fatal("verification must fail when event_count is altered")
	}
	if ed25519.Verify(pub, CanonicalCheckpointMessage("tenant-1", "TAMPERED", 42), sig) {
		t.Fatal("verification must fail when head_hash is altered")
	}
}

func TestSigning_DisabledByDefault(t *testing.T) {
	svc := New(Config{Logger: zerolog.Nop()})
	if svc.SigningKey().Enabled {
		t.Fatal("signing should be disabled when no key is configured")
	}
	if _, err := svc.CreateCheckpoint(context.Background(), "00000000-0000-0000-0000-000000000000"); err != ErrSigningDisabled {
		t.Fatalf("want ErrSigningDisabled, got %v", err)
	}
}

func TestSigning_InvalidKeyDisables(t *testing.T) {
	// A malformed key must fail closed (disabled), not panic.
	svc := New(Config{SigningKeySeedB64: "not-base64-!!", Logger: zerolog.Nop()})
	if svc.SigningKey().Enabled {
		t.Fatal("malformed signing key should leave signing disabled")
	}
}
