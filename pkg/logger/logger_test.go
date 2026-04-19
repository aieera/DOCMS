package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The logger's context-aware helpers put tenant/correlation ids on every
// log line. Any regression in that wiring hides breadcrumbs during an
// incident — pin the contract.

func TestLogger_InfoIncludesServiceAndVersion(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewWithWriter(buf, "svc-x", "v1.2.3", "info")
	l.Info(context.Background()).Msg("hello")

	var row map[string]any
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("not JSON: %s", buf.String())
	}
	if row["service"] != "svc-x" {
		t.Errorf("service: %v", row["service"])
	}
	if row["version"] != "v1.2.3" {
		t.Errorf("version: %v", row["version"])
	}
	if row["message"] != "hello" {
		t.Errorf("message: %v", row["message"])
	}
}

func TestLogger_LevelFilter(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewWithWriter(buf, "svc", "v", "error")
	l.Info(context.Background()).Msg("suppressed")
	l.Warn(context.Background()).Msg("also suppressed")
	l.Error(context.Background()).Msg("kept")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line at error level, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `"kept"`) {
		t.Errorf("wrong line survived: %s", lines[0])
	}
}

func TestLogger_TenantAndCorrelationFromCtx(t *testing.T) {
	buf := &bytes.Buffer{}
	l := NewWithWriter(buf, "svc", "v", "info")

	ctx := context.WithValue(context.Background(), TenantIDKey, "tenant-abc")
	ctx = context.WithValue(ctx, CorrelationIDKey, "corr-xyz")
	ctx = context.WithValue(ctx, UserIDKey, "user-42")

	l.Info(ctx).Msg("event")

	var row map[string]any
	_ = json.Unmarshal(buf.Bytes(), &row)
	if row["tenant_id"] != "tenant-abc" {
		t.Errorf("tenant_id: %v", row["tenant_id"])
	}
	if row["correlation_id"] != "corr-xyz" {
		t.Errorf("correlation_id: %v", row["correlation_id"])
	}
	if row["user_id"] != "user-42" {
		t.Errorf("user_id: %v", row["user_id"])
	}
}

// ---- PII scrubbing ---------------------------------------------------------

func TestHashEmail_StableDigest(t *testing.T) {
	// Case and whitespace should normalize before hashing so analytics
	// joins work regardless of how the email was captured.
	a := HashEmail("Alice@Example.com")
	b := HashEmail("  alice@example.com  ")
	if a == "" {
		t.Fatal("valid email should hash")
	}
	if a != b {
		t.Errorf("normalization failed: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("missing algo prefix: %s", a)
	}
	// Hash bucket is first 12 bytes of SHA-256 → 24 hex chars.
	if len(strings.TrimPrefix(a, "sha256:")) != 24 {
		t.Errorf("hash length: %d hex chars, want 24", len(strings.TrimPrefix(a, "sha256:")))
	}
}

func TestHashEmail_EmptyAndInvalid(t *testing.T) {
	if got := HashEmail(""); got != "" {
		t.Errorf("empty → empty, got %q", got)
	}
	if got := HashEmail("not-an-email"); got != "invalid" {
		t.Errorf("malformed → 'invalid', got %q", got)
	}
	if got := HashEmail("@nodomain"); got != "invalid" {
		t.Errorf("malformed → 'invalid', got %q", got)
	}
}

func TestHashEmail_NeverContainsPlaintext(t *testing.T) {
	h := HashEmail("alice@example.com")
	if strings.Contains(h, "alice") || strings.Contains(h, "example") {
		t.Errorf("hash must not echo plaintext: %s", h)
	}
}

func TestHashIP_V4PreservesSubnet(t *testing.T) {
	// The final octet is zeroed before hashing → any two IPs in the same
	// /24 must hash to the same bucket, different /24s differ.
	a := HashIP("192.168.1.42")
	b := HashIP("192.168.1.99")
	c := HashIP("192.168.2.42")
	if a == "" {
		t.Fatal("valid IP should hash")
	}
	if a != b {
		t.Errorf("same /24 must collide: %s vs %s", a, b)
	}
	if a == c {
		t.Error("different /24 must diverge")
	}
}

func TestHashIP_V6Hashed(t *testing.T) {
	h := HashIP("2001:db8::1")
	if h == "" || h == "invalid" {
		t.Errorf("v6 should hash: got %q", h)
	}
	if strings.Contains(h, "2001") {
		t.Error("hash leaks plaintext v6 prefix")
	}
}

func TestHashIP_EmptyAndInvalid(t *testing.T) {
	if got := HashIP(""); got != "" {
		t.Errorf("empty → empty, got %q", got)
	}
	if got := HashIP("not-an-ip"); got != "invalid" {
		t.Errorf("malformed → 'invalid', got %q", got)
	}
}

func TestParseLevel_Aliases(t *testing.T) {
	if parseLevel("debug").String() != "debug" {
		t.Error("debug")
	}
	if parseLevel("DEBUG").String() != "debug" {
		t.Error("DEBUG (case)")
	}
	if parseLevel("warn").String() != "warn" {
		t.Error("warn")
	}
	if parseLevel("warning").String() != "warn" {
		t.Error("warning alias should map to warn")
	}
	if parseLevel("gibberish").String() != "info" {
		t.Error("unknown level must fall back to info")
	}
}
