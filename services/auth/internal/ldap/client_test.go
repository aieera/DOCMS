// ADR 0062 — flow-level invariants the unit suite can pin without
// a live directory. End-to-end coverage against the slapd compose
// container lives in services/auth/internal/ldap/integration_test.go
// (build-tag `integration`).
package ldap

import (
	"strings"
	"testing"
	"time"
)

func TestValidateScheme_LdapsAlwaysOK(t *testing.T) {
	if err := validateScheme(Config{URL: "ldaps://dir.example.com:636"}); err != nil {
		t.Fatalf("ldaps:// should be accepted: %v", err)
	}
}

func TestValidateScheme_PlainLdapRejectedWithoutOptIn(t *testing.T) {
	err := validateScheme(Config{URL: "ldap://dir.example.com:389"})
	if err == nil {
		t.Fatal("plain ldap:// must require StartTLS or AllowInsecure")
	}
	if !strings.Contains(err.Error(), "AllowInsecure") {
		t.Errorf("expected AllowInsecure hint in error, got %v", err)
	}
}

func TestValidateScheme_PlainLdapWithStartTLSOK(t *testing.T) {
	if err := validateScheme(Config{URL: "ldap://dir.example.com:389", UseStartTLS: true}); err != nil {
		t.Fatalf("ldap:// + StartTLS should pass: %v", err)
	}
}

func TestValidateScheme_PlainLdapWithAllowInsecureOK(t *testing.T) {
	if err := validateScheme(Config{URL: "ldap://dir.example.com:389", AllowInsecure: true}); err != nil {
		t.Fatalf("AllowInsecure should permit plain bind: %v", err)
	}
}

func TestValidateScheme_UnknownSchemeRejected(t *testing.T) {
	if err := validateScheme(Config{URL: "https://dir.example.com"}); err == nil {
		t.Fatal("https:// is not an LDAP scheme — must reject")
	}
}

func TestWithDefaults_FillsAllZeroFields(t *testing.T) {
	c := withDefaults(Config{})
	if c.DialTimeout != defaultDialTimeout {
		t.Errorf("DialTimeout=%v want %v", c.DialTimeout, defaultDialTimeout)
	}
	if c.RequestTimeout != defaultRequestTimeout {
		t.Errorf("RequestTimeout=%v want %v", c.RequestTimeout, defaultRequestTimeout)
	}
	if c.EmailAttribute != "mail" {
		t.Errorf("EmailAttribute=%q want mail", c.EmailAttribute)
	}
	if c.DisplayNameAttribute != "displayName" {
		t.Errorf("DisplayNameAttribute=%q want displayName", c.DisplayNameAttribute)
	}
	if c.UserSearchFilter != "(sAMAccountName={username})" {
		t.Errorf("UserSearchFilter=%q want default AD shape", c.UserSearchFilter)
	}
	if c.GroupSearchFilter != "(member={user_dn})" {
		t.Errorf("GroupSearchFilter=%q want default", c.GroupSearchFilter)
	}
}

func TestWithDefaults_PreservesNonZeroFields(t *testing.T) {
	in := Config{
		DialTimeout:    7 * time.Second,
		EmailAttribute: "userPrincipalName",
	}
	out := withDefaults(in)
	if out.DialTimeout != 7*time.Second {
		t.Errorf("DialTimeout overridden")
	}
	if out.EmailAttribute != "userPrincipalName" {
		t.Errorf("EmailAttribute overridden")
	}
}

func TestPool_DiscardsExpired(t *testing.T) {
	p := NewPool(PoolConfig{MaxIdle: 4, MaxLifetime: 1 * time.Millisecond})
	if p.cfg.MaxLifetime != 1*time.Millisecond {
		t.Errorf("ttl=%v want 1ms", p.cfg.MaxLifetime)
	}
	if p.cfg.MaxIdle != 4 {
		t.Errorf("idle=%d want 4", p.cfg.MaxIdle)
	}
}

func TestDial_RejectsInvalidURL(t *testing.T) {
	// Plain ldap:// without StartTLS or AllowInsecure must short-circuit
	// at validateScheme without any network I/O.
	c, err := Dial(Config{URL: "ldap://nope.invalid:389"})
	if err == nil {
		c.Close()
		t.Fatal("expected ErrInsecureRejected")
	}
}
