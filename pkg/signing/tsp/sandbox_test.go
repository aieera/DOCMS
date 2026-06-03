//go:build sandbox

// Sandbox smoke harness for the QES TSP adapters.
//
// Tier A only: every QTSP we surveyed requires real eID-grade
// authentication for the Sign() step (a national eID, MobileID
// push, or video-ident workflow). That can't be automated without
// a human at the QTSP side, so we limit the harness to:
//
//   1. Register — provision (or look up) the signer identity.
//   2. Authorize — open a transaction for a fake document hash and
//      get back a redirect URL.
//
// That's enough to assert: OAuth/mTLS works, the request shape we
// build hasn't drifted from the QTSP's spec, the response shape we
// parse hasn't drifted either. Tier B (full round-trip) is run
// manually from a developer machine with a human completing the
// QTSP-side authentication.
//
// Credential layout (sealed GitHub Actions env "esign-sandbox"):
//
//   INTESI_SANDBOX_BASE_URL
//   INTESI_SANDBOX_CLIENT_ID
//   INTESI_SANDBOX_CLIENT_SECRET
//   INTESI_SANDBOX_PINNED_CA_PEM   (path or inline; their sandbox CA isn't on Mozilla's bundle)
//   INTESI_SANDBOX_SIGNER_EMAIL
//
//   INFOCERT_SANDBOX_BASE_URL
//   INFOCERT_SANDBOX_CLIENT_ID
//   INFOCERT_SANDBOX_CLIENT_SECRET
//   INFOCERT_SANDBOX_ORG_ID
//   INFOCERT_SANDBOX_SIGNER_EMAIL
//
//   SWISSCOM_SANDBOX_BASE_URL
//   SWISSCOM_SANDBOX_CUSTOMER_ID
//   SWISSCOM_SANDBOX_CLIENT_CERT_PEM   (file path)
//   SWISSCOM_SANDBOX_CLIENT_KEY_PEM    (file path)
//   SWISSCOM_SANDBOX_SIGNER_EMAIL
package tsp_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aieera/sedoc/pkg/signing/tsp"
)

// ----- Intesi ------------------------------------------------------

func TestSandbox_Intesi_TierA(t *testing.T) {
	c, err := tsp.NewIntesi(tsp.IntesiConfig{
		BaseURL:      mustEnvSandbox(t, "INTESI_SANDBOX_BASE_URL"),
		ClientID:     mustEnvSandbox(t, "INTESI_SANDBOX_CLIENT_ID"),
		ClientSecret: mustEnvSandbox(t, "INTESI_SANDBOX_CLIENT_SECRET"),
		PinnedCAPEM:  os.Getenv("INTESI_SANDBOX_PINNED_CA_PEM"),
	})
	if err != nil {
		t.Fatalf("new intesi: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	email := mustEnvSandbox(t, "INTESI_SANDBOX_SIGNER_EMAIL")
	reg, err := c.Register(ctx, tsp.RegisterReq{
		TenantID: "sandbox-tenant", SignerEmail: email,
		SignerName: "SeDoc Sandbox Signer", CountryCode: "IT",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.SubjectID == "" {
		t.Fatal("subject_id empty")
	}
	t.Logf("subject_id=%q", reg.SubjectID)

	auth, err := c.Authorize(ctx, tsp.AuthorizeReq{
		TenantID: "sandbox-tenant", SubjectID: reg.SubjectID,
		DocumentHash: fakeDocHash(),
		HashAlgo: "SHA-256",
		ReturnURL: "https://example.invalid/return?session=smoke",
		SignerEmail: email,
		Reason: "vaultdms sandbox smoke test",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if auth.RedirectURL == "" {
		t.Fatal("redirect_url empty")
	}
	if auth.ExternalID == "" {
		t.Fatal("external_id empty")
	}
	t.Logf("redirect=%s external_id=%s expires=%s", auth.RedirectURL, auth.ExternalID, auth.ExpiresAt)
}

// ----- InfoCert ----------------------------------------------------

func TestSandbox_InfoCert_TierA(t *testing.T) {
	c, err := tsp.NewInfoCert(tsp.InfoCertConfig{
		BaseURL:      mustEnvSandbox(t, "INFOCERT_SANDBOX_BASE_URL"),
		ClientID:     mustEnvSandbox(t, "INFOCERT_SANDBOX_CLIENT_ID"),
		ClientSecret: mustEnvSandbox(t, "INFOCERT_SANDBOX_CLIENT_SECRET"),
		OrgID:        mustEnvSandbox(t, "INFOCERT_SANDBOX_ORG_ID"),
	})
	if err != nil {
		t.Fatalf("new infocert: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	email := mustEnvSandbox(t, "INFOCERT_SANDBOX_SIGNER_EMAIL")
	reg, err := c.Register(ctx, tsp.RegisterReq{
		TenantID: "sandbox-tenant", SignerEmail: email,
		SignerName: "SeDoc Sandbox Signer", CountryCode: "IT",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Logf("subject_id=%q", reg.SubjectID)

	auth, err := c.Authorize(ctx, tsp.AuthorizeReq{
		TenantID: "sandbox-tenant", SubjectID: reg.SubjectID,
		DocumentHash: fakeDocHash(), HashAlgo: "SHA-256",
		ReturnURL: "https://example.invalid/return?session=smoke",
		SignerEmail: email,
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if auth.RedirectURL == "" || auth.ExternalID == "" {
		t.Fatalf("authorize missing fields: %+v", auth)
	}
	t.Logf("redirect=%s external_id=%s", auth.RedirectURL, auth.ExternalID)
}

// ----- Swisscom ----------------------------------------------------

func TestSandbox_Swisscom_TierA(t *testing.T) {
	certPath := mustEnvSandbox(t, "SWISSCOM_SANDBOX_CLIENT_CERT_PEM")
	keyPath := mustEnvSandbox(t, "SWISSCOM_SANDBOX_CLIENT_KEY_PEM")
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read client cert: %v", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read client key: %v", err)
	}
	c, err := tsp.NewSwisscom(tsp.SwisscomConfig{
		BaseURL:       mustEnvSandbox(t, "SWISSCOM_SANDBOX_BASE_URL"),
		CustomerID:    mustEnvSandbox(t, "SWISSCOM_SANDBOX_CUSTOMER_ID"),
		ClientCertPEM: string(certPEM),
		ClientKeyPEM:  string(keyPEM),
	})
	if err != nil {
		t.Fatalf("new swisscom: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	email := mustEnvSandbox(t, "SWISSCOM_SANDBOX_SIGNER_EMAIL")
	if _, err := c.Register(ctx, tsp.RegisterReq{
		TenantID: "sandbox-tenant", SignerEmail: email,
		SignerName: "SeDoc Sandbox Signer", CountryCode: "CH",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	auth, err := c.Authorize(ctx, tsp.AuthorizeReq{
		TenantID: "sandbox-tenant", SubjectID: email,
		DocumentHash: fakeDocHash(), HashAlgo: "SHA-256",
		ReturnURL: "https://example.invalid/return?session=smoke",
		SignerEmail: email,
		Reason: "vaultdms sandbox smoke",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if auth.RedirectURL == "" {
		t.Fatal("redirect_url empty")
	}
	t.Logf("redirect=%s external_id=%s", auth.RedirectURL, auth.ExternalID)
}

// ----- helpers -----------------------------------------------------

func mustEnvSandbox(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set — skipping sandbox test", key)
	}
	return v
}

// fakeDocHash returns a stable 64-char hex SHA-256. The QTSPs don't
// actually verify the hash matches a real document at Authorize
// time — they just attach it to the transaction.
func fakeDocHash() string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
