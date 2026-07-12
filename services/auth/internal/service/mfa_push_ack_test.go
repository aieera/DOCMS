// Push-MFA signed-ack verification (ADR 0122).
//
// Regression context: VerifyPushChallenge accepted ANY non-empty ack —
// the holder of the MFA session token (i.e. a stolen password) could
// approve their own push challenge without possessing a device. The
// unit tests here need no infrastructure; the flow tests (build tag
// `integration`) drive validatePushAck against real Postgres + Redis.
package service

import (
	"strings"
	"testing"
)

func TestParsePushAck(t *testing.T) {
	cases := []struct {
		in         string
		wantDev    string
		wantSig    string
		wantOK     bool
		annotation string
	}{
		{"dev-1.abcd", "dev-1", "abcd", true, "well-formed"},
		{"no-separator", "", "", false, "missing separator"},
		{".sig-only", "", "", false, "empty device id"},
		{"dev-only.", "", "", false, "empty signature"},
		{"", "", "", false, "empty ack"},
		{"a.b.c", "a", "b.c", true, "first dot splits; rest is signature"},
	}
	for _, c := range cases {
		dev, sig, ok := parsePushAck(c.in)
		if ok != c.wantOK || dev != c.wantDev || sig != c.wantSig {
			t.Errorf("%s: parsePushAck(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.annotation, c.in, dev, sig, ok, c.wantDev, c.wantSig, c.wantOK)
		}
	}
}

func TestSignPushAck_ReferenceProperties(t *testing.T) {
	sig := signPushAck("key-1", "chal-1", "dev-1")
	if len(sig) != 64 || strings.ToLower(sig) != sig {
		t.Fatalf("signature must be 64 lowercase hex chars, got %q", sig)
	}
	if sig != signPushAck("key-1", "chal-1", "dev-1") {
		t.Fatal("signature must be deterministic")
	}
	// Binding: any component change must change the signature.
	if signPushAck("key-2", "chal-1", "dev-1") == sig {
		t.Fatal("different key must produce a different signature")
	}
	if signPushAck("key-1", "chal-2", "dev-1") == sig {
		t.Fatal("different challenge must produce a different signature (challenge binding)")
	}
	if signPushAck("key-1", "chal-1", "dev-2") == sig {
		t.Fatal("different device must produce a different signature (device binding)")
	}
}
