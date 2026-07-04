package model

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func baseContainer() IRMContainer {
	return IRMContainer{
		TenantID: uuid.New(), ID: uuid.New(),
		AllowedActions: []string{IRMActionView, IRMActionPrint},
		ExpiresAt:      time.Now().Add(24 * time.Hour),
	}
}

func TestValidateLicenseOpen_EmailRecipientOK(t *testing.T) {
	c := baseContainer()
	lic := IRMLicense{RecipientType: IRMRecipientEmail, RecipientRef: "bob@acme.com"}
	if r := ValidateLicenseOpen(c, lic, time.Now(), uuid.Nil); r != LicenseOK {
		t.Fatalf("email recipient should open without session, got %q", r)
	}
}

func TestValidateLicenseOpen_RevokedLicenseBlocks(t *testing.T) {
	// The DoD: revoking the license blocks the next open.
	c := baseContainer()
	now := time.Now()
	lic := IRMLicense{RecipientType: IRMRecipientEmail, RecipientRef: "bob@acme.com", RevokedAt: &now}
	if r := ValidateLicenseOpen(c, lic, now, uuid.Nil); r != LicenseRevoked {
		t.Fatalf("revoked license must block, got %q", r)
	}
}

func TestValidateLicenseOpen_RevokedContainerBlocks(t *testing.T) {
	c := baseContainer()
	now := time.Now()
	c.RevokedAt = &now
	lic := IRMLicense{RecipientType: IRMRecipientEmail, RecipientRef: "bob@acme.com"}
	if r := ValidateLicenseOpen(c, lic, now, uuid.Nil); r != LicenseRevoked {
		t.Fatalf("revoked container must block all licenses, got %q", r)
	}
}

func TestValidateLicenseOpen_Expired(t *testing.T) {
	c := baseContainer()
	c.ExpiresAt = time.Now().Add(-time.Hour)
	lic := IRMLicense{RecipientType: IRMRecipientEmail, RecipientRef: "bob@acme.com"}
	if r := ValidateLicenseOpen(c, lic, time.Now(), uuid.Nil); r != LicenseExpired {
		t.Fatalf("expired must block, got %q", r)
	}
}

func TestValidateLicenseOpen_InternalRequiresMatchingSession(t *testing.T) {
	c := baseContainer()
	uid := uuid.New()
	lic := IRMLicense{RecipientType: IRMRecipientUser, RecipientRef: uid.String()}
	// no session -> session_required
	if r := ValidateLicenseOpen(c, lic, time.Now(), uuid.Nil); r != LicenseSessionRequired {
		t.Fatalf("internal-bound without session must be session_required, got %q", r)
	}
	// wrong user -> session_required (a leaked token can't open under another user)
	if r := ValidateLicenseOpen(c, lic, time.Now(), uuid.New()); r != LicenseSessionRequired {
		t.Fatalf("internal-bound with wrong session must be session_required, got %q", r)
	}
	// matching user -> OK
	if r := ValidateLicenseOpen(c, lic, time.Now(), uid); r != LicenseOK {
		t.Fatalf("internal-bound with matching session must open, got %q", r)
	}
}

func TestActionAllowed(t *testing.T) {
	c := baseContainer() // view + print
	if !c.ActionAllowed(IRMActionView) || !c.ActionAllowed(IRMActionPrint) {
		t.Fatal("view+print should be allowed")
	}
	if c.ActionAllowed(IRMActionDownload) {
		t.Fatal("download not granted; must be denied")
	}
}

func TestPolicyHeaderSignVerify(t *testing.T) {
	key := []byte("test-irm-signing-key")
	h := IRMPolicyHeader{
		Format: IRMFormat, ContainerID: uuid.New().String(), TenantID: uuid.New().String(),
		Title: "Q3 board deck", Mime: "application/pdf",
		AllowedActions: []string{IRMActionView}, ExpiresAt: time.Now().Add(time.Hour).UTC(),
		RecipientType: IRMRecipientEmail, RecipientRef: "bob@acme.com",
		LicenseToken: "tok_abc", CallbackURL: "/api/v1/irm/licenses/check", IssuedAt: time.Now().UTC(),
	}
	if err := h.Sign(key); err != nil {
		t.Fatal(err)
	}
	if !h.Verify(key) {
		t.Fatal("valid signature must verify")
	}
	// tamper: widening actions must invalidate the signature
	tampered := h
	tampered.AllowedActions = []string{IRMActionView, IRMActionDownload}
	if tampered.Verify(key) {
		t.Fatal("tampered header must not verify")
	}
	// wrong key must not verify
	if h.Verify([]byte("wrong-key")) {
		t.Fatal("wrong key must not verify")
	}
}
