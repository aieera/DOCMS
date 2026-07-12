package sso

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestCertFingerprint_Format(t *testing.T) {
	m, err := NewSelfSignedSP()
	if err != nil {
		t.Fatal(err)
	}
	fp := CertFingerprint(m.Certificate)
	// SHA-256 → 32 bytes → 32 colon-separated upper-hex octets.
	parts := strings.Split(fp, ":")
	if len(parts) != 32 {
		t.Fatalf("fingerprint has %d octets, want 32: %q", len(parts), fp)
	}
	for _, p := range parts {
		if len(p) != 2 || strings.ToUpper(p) != p {
			t.Fatalf("bad fingerprint octet %q in %q", p, fp)
		}
	}
	// Deterministic for a given cert.
	if fp != CertFingerprint(m.Certificate) {
		t.Fatal("fingerprint must be deterministic")
	}
}

func spPEMs(t *testing.T) (keyPEM, certPEM string) {
	t.Helper()
	m, err := NewSelfSignedSP()
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(m.PrivateKey),
	}))
	certPEM = string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: m.Certificate.Raw,
	}))
	return keyPEM, certPEM
}

func TestResolveSPKeyMaterial_InjectedPEMsWin(t *testing.T) {
	keyPEM, certPEM := spPEMs(t)
	// Injected PEMs are used verbatim — no pool touched.
	m, err := ResolveSPKeyMaterial(context.Background(), nil, nil, keyPEM, certPEM, zerolog.Nop())
	if err != nil {
		t.Fatalf("resolve injected: %v", err)
	}
	// Round-trips to the same cert.
	wantCert, _ := pem.Decode([]byte(certPEM))
	if string(m.Certificate.Raw) != string(wantCert.Bytes) {
		t.Fatal("injected cert must be used verbatim")
	}
}

func TestResolveSPKeyMaterial_NoKEKFallsBackEphemeral(t *testing.T) {
	// No injected PEMs, no usable KEK → ephemeral self-signed (dev mode).
	// Must not touch the (nil) pool. Two calls give DIFFERENT certs —
	// that's the ephemeral property (and exactly why it's warned about).
	m1, err := ResolveSPKeyMaterial(context.Background(), nil, nil, "", "", zerolog.Nop())
	if err != nil {
		t.Fatalf("ephemeral resolve: %v", err)
	}
	m2, err := ResolveSPKeyMaterial(context.Background(), nil, []byte("short-kek"), "", "", zerolog.Nop())
	if err != nil {
		t.Fatalf("ephemeral resolve (short kek): %v", err)
	}
	if CertFingerprint(m1.Certificate) == CertFingerprint(m2.Certificate) {
		t.Fatal("ephemeral certs should differ per call (no durable store without a KEK)")
	}
}
