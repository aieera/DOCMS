package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/services/document/internal/model"
)

func newLocalKM(t *testing.T) pkgcrypto.KeyManager {
	t.Helper()
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	km, err := pkgcrypto.NewLocalKeyManager(base64.StdEncoding.EncodeToString(master), nil)
	if err != nil {
		t.Fatal(err)
	}
	return km
}

// The IRM crypto core: seal a payload under a fresh DEK, wrap that DEK per
// recipient, and prove (a) each recipient recovers the payload and (b) one
// recipient's wrapped key does NOT unwrap under another recipient's context
// (per-recipient isolation). No DB needed — this is the DoD crypto path.
func TestIRMSealUnsealAndPerRecipientIsolation(t *testing.T) {
	ctx := context.Background()
	km := newLocalKM(t)

	payload := []byte("Q3 board deck — highly confidential")
	dek, err := pkgcrypto.GenerateDEK()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, nonce, err := pkgcrypto.EncryptData(payload, dek)
	if err != nil {
		t.Fatal(err)
	}

	tenantID := uuid.New()
	licA, licB := uuid.New(), uuid.New()
	refA := model.IRMLicenseKeyRef(tenantID, licA)
	refB := model.IRMLicenseKeyRef(tenantID, licB)

	wrappedA, err := km.EncryptDataKey(ctx, refA, dek)
	if err != nil {
		t.Fatal(err)
	}
	wrappedB, err := km.EncryptDataKey(ctx, refB, dek)
	if err != nil {
		t.Fatal(err)
	}

	// Each recipient unwraps under their own context and recovers the payload.
	for _, tc := range []struct {
		ref     string
		wrapped []byte
	}{{refA, wrappedA}, {refB, wrappedB}} {
		got, err := km.DecryptDataKey(ctx, tc.ref, tc.wrapped)
		if err != nil {
			t.Fatalf("unwrap under own context: %v", err)
		}
		pt, err := pkgcrypto.DecryptData(ciphertext, nonce, got)
		if err != nil {
			t.Fatalf("decrypt payload: %v", err)
		}
		if !bytes.Equal(pt, payload) {
			t.Fatal("recovered payload mismatch")
		}
	}

	// Isolation: recipient A's wrapped DEK must NOT unwrap under B's context.
	if _, err := km.DecryptDataKey(ctx, refB, wrappedA); err == nil {
		t.Fatal("recipient A's wrapped key must not unwrap under recipient B's context (fails-closed)")
	}

	// A revoked recipient is enforced above the crypto layer (ValidateLicenseOpen);
	// the wrap itself stays recoverable, which is why revocation must gate the
	// online callback — asserted in model/irm_test.go.
}

func TestIRMSealDetectsTamperedCiphertext(t *testing.T) {
	dek, _ := pkgcrypto.GenerateDEK()
	ct, nonce, err := pkgcrypto.EncryptData([]byte("secret"), dek)
	if err != nil {
		t.Fatal(err)
	}
	ct[0] ^= 0xff // flip a byte
	if _, err := pkgcrypto.DecryptData(ct, nonce, dek); err == nil {
		t.Fatal("AES-GCM must reject tampered ciphertext")
	}
}
