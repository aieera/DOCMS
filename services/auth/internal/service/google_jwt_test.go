package service

import "testing"

// newGoogleVerifier + configured() are env-driven; verify they gate the
// feature on both the audience and a non-empty hosted-domain allow-list.
func TestGoogleVerifier_Configured(t *testing.T) {
	cases := []struct {
		name, aud, hds string
		want           bool
	}{
		{"both set", "client-123.apps.googleusercontent.com", "acme.com", true},
		{"missing audience", "", "acme.com", false},
		{"missing hds", "client-123", "", false},
		{"blank hds", "client-123", " , ,", false},
		{"neither", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envGoogleAudience, c.aud)
			t.Setenv(envGoogleAllowedHDs, c.hds)
			if got := newGoogleVerifier().configured(); got != c.want {
				t.Fatalf("configured() = %v, want %v", got, c.want)
			}
		})
	}
}

// The allow-list must be parsed case-insensitively and tolerate padding.
func TestGoogleVerifier_AllowListParsing(t *testing.T) {
	t.Setenv(envGoogleAudience, "client-123")
	t.Setenv(envGoogleAllowedHDs, " Acme.com , Beta.io ")
	v := newGoogleVerifier()
	for _, hd := range []string{"acme.com", "beta.io"} {
		if _, ok := v.allowedHDs[hd]; !ok {
			t.Fatalf("expected %q in allow-list, got %v", hd, v.allowedHDs)
		}
	}
	if _, ok := v.allowedHDs["gamma.net"]; ok {
		t.Fatal("gamma.net should not be present")
	}
}

func newTestGoogleVerifier(hds ...string) *googleVerifier {
	v := &googleVerifier{audience: "client-123", allowedHDs: map[string]struct{}{}}
	for _, hd := range hds {
		v.allowedHDs[hd] = struct{}{}
	}
	return v
}

// checkClaims is the SeDoc policy layer applied after go-oidc's crypto
// verification. It is the security-critical gate: only a verified email
// on an allow-listed Workspace domain may map to a SeDoc user.
func TestGoogleVerifier_CheckClaims(t *testing.T) {
	v := newTestGoogleVerifier("acme.com")

	t.Run("happy path", func(t *testing.T) {
		got, err := v.checkClaims(googleClaims{
			Sub: "sub-1", Email: "Alice@Acme.com", EmailVerified: true, HD: "Acme.com",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Email != "alice@acme.com" {
			t.Fatalf("email = %q, want lowercased alice@acme.com", got.Email)
		}
		if got.HD != "acme.com" || got.Sub != "sub-1" {
			t.Fatalf("unexpected identity %+v", got)
		}
	})

	t.Run("unverified email rejected", func(t *testing.T) {
		if _, err := v.checkClaims(googleClaims{
			Sub: "s", Email: "a@acme.com", EmailVerified: false, HD: "acme.com",
		}); err == nil {
			t.Fatal("expected rejection for email_verified=false")
		}
	})

	t.Run("missing hd rejected (personal account)", func(t *testing.T) {
		if _, err := v.checkClaims(googleClaims{
			Sub: "s", Email: "a@gmail.com", EmailVerified: true, HD: "",
		}); err == nil {
			t.Fatal("expected rejection for empty hd")
		}
	})

	t.Run("hd not on allow-list rejected", func(t *testing.T) {
		if _, err := v.checkClaims(googleClaims{
			Sub: "s", Email: "a@evil.com", EmailVerified: true, HD: "evil.com",
		}); err == nil {
			t.Fatal("expected rejection for non-allow-listed hd")
		}
	})

	t.Run("empty email rejected", func(t *testing.T) {
		if _, err := v.checkClaims(googleClaims{
			Sub: "s", Email: "", EmailVerified: true, HD: "acme.com",
		}); err == nil {
			t.Fatal("expected rejection for empty email")
		}
	})

	t.Run("empty sub rejected", func(t *testing.T) {
		if _, err := v.checkClaims(googleClaims{
			Sub: "  ", Email: "a@acme.com", EmailVerified: true, HD: "acme.com",
		}); err == nil {
			t.Fatal("expected rejection for empty sub")
		}
	})
}

// verify must fail closed when the feature is unconfigured, before any
// network call to Google's discovery endpoint.
func TestGoogleVerifier_UnconfiguredRefuses(t *testing.T) {
	v := &googleVerifier{allowedHDs: map[string]struct{}{}}
	if _, err := v.verify(t.Context(), "any.token.here"); err == nil {
		t.Fatal("expected error from an unconfigured verifier")
	}
}

// email_verified must decode from both the JSON boolean and the string
// form, and reject anything else (failing closed).
func TestJSONBool(t *testing.T) {
	cases := []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{"true", true, false},
		{`"true"`, true, false},
		{"false", false, false},
		{`"false"`, false, false},
		{"null", false, false},
		{`"maybe"`, false, true},
		{"1", false, true},
	}
	for _, c := range cases {
		var b jsonBool
		err := b.UnmarshalJSON([]byte(c.in))
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.in, err)
		}
		if bool(b) != c.want {
			t.Fatalf("%s: got %v want %v", c.in, bool(b), c.want)
		}
	}
}
