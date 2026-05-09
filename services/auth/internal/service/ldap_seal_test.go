// ADR 0062 — bind-password seal/unseal must round-trip under the
// service's local-KEK path. A key swap must fail-closed.
package service

import (
	"crypto/rand"
	"testing"

	"github.com/vaultdms/vaultdms/pkg/crypto"
)

func TestSealBindPassword_Roundtrip(t *testing.T) {
	kek := make([]byte, crypto.DEKSize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	s := &Service{localKek: kek}

	const plain = "super-secret-bind-password"
	sealed, err := s.SealBindPassword(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if string(sealed) == plain {
		t.Fatal("sealed equals plaintext — encryption no-op")
	}

	got, err := s.unsealBindPassword(sealed)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if got != plain {
		t.Errorf("got %q want %q", got, plain)
	}
}

func TestSealBindPassword_RejectsEmpty(t *testing.T) {
	s := &Service{localKek: make([]byte, crypto.DEKSize)}
	if _, err := s.SealBindPassword(""); err == nil {
		t.Fatal("empty plaintext must error")
	}
}

func TestUnsealBindPassword_KEKSwapFails(t *testing.T) {
	kek1 := make([]byte, crypto.DEKSize)
	if _, err := rand.Read(kek1); err != nil {
		t.Fatal(err)
	}
	s := &Service{localKek: kek1}
	sealed, err := s.SealBindPassword("plaintext-payload")
	if err != nil {
		t.Fatal(err)
	}
	// Different KEK — decrypt must fail-closed, never return wrong plaintext.
	kek2 := make([]byte, crypto.DEKSize)
	if _, err := rand.Read(kek2); err != nil {
		t.Fatal(err)
	}
	s.localKek = kek2
	if _, err := s.unsealBindPassword(sealed); err == nil {
		t.Fatal("unseal under wrong KEK must error")
	}
}
