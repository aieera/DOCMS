package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// signPayload is the HMAC used on every outbound webhook delivery. Pinning
// the exact algorithm here catches accidental scheme drift (e.g. dropping
// the timestamp, switching to SHA-512, etc.) that would silently break
// receivers validating the signature.

func TestSignPayload_DeterministicAndMatchesHMACSHA256(t *testing.T) {
	secret := "whsec_test_secret"
	ts := "1700000000"
	payload := []byte(`{"event":"doc.created","id":"d1"}`)

	got := signPayload(secret, ts, payload)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Fatalf("signature drift:\n got  %s\n want %s", got, want)
	}
	if !strings.HasPrefix(got, "sha256=") {
		t.Errorf("signature must be prefixed with 'sha256=' for receiver compat")
	}
}

func TestSignPayload_ChangesWithTimestamp(t *testing.T) {
	// Replay defence: the same payload signed at a different time MUST
	// produce a different signature so receivers can reject replayed
	// timestamps without false positives.
	secret := "whsec"
	payload := []byte(`hello`)
	a := signPayload(secret, "1", payload)
	b := signPayload(secret, "2", payload)
	if a == b {
		t.Fatal("signatures must differ when timestamp changes")
	}
}

func TestSignPayload_ChangesWithSecret(t *testing.T) {
	payload := []byte(`hello`)
	a := signPayload("secretA", "1700000000", payload)
	b := signPayload("secretB", "1700000000", payload)
	if a == b {
		t.Fatal("signatures must differ when secret changes")
	}
}

func TestValidateURL_RejectsNonHTTPS(t *testing.T) {
	err := ValidateURL("http://example.com/hook", false)
	if err == nil {
		t.Fatal("plain http must be rejected")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error should mention https: %v", err)
	}
}

func TestValidateURL_RejectsMalformed(t *testing.T) {
	// url.Parse is lenient — an invalid URL is mostly a scheme-less string.
	// Anything without https should fail the https check.
	err := ValidateURL("not-a-url", false)
	if err == nil {
		t.Fatal("malformed URL must be rejected")
	}
}

func TestValidateURL_AllowPrivateWaivesHTTPSAndPrivateIPChecks(t *testing.T) {
	// On-prem mode (SEDOC_WEBHOOK_ALLOW_PRIVATE): a plain-http private-IP
	// target must pass the scheme + private-IP guards. We can't assert the
	// HEAD reachability succeeds (no live server in a unit test), so assert
	// only that failure — if any — is the reachability check, never the
	// https/private-IP guard the flag is meant to waive.
	err := ValidateURL("http://192.168.70.79:8080/", true)
	if err != nil {
		if strings.Contains(err.Error(), "https") || strings.Contains(err.Error(), "internal IP") {
			t.Fatalf("allowPrivate must waive https + private-IP checks, got: %v", err)
		}
	}
}

func TestValidateURL_RejectsNonHTTPSchemeEvenWithAllowPrivate(t *testing.T) {
	// allowPrivate only widens to http — exotic schemes stay rejected.
	err := ValidateURL("ftp://192.168.70.79/hook", true)
	if err == nil {
		t.Fatal("non-http(s) scheme must be rejected even with allowPrivate")
	}
}
