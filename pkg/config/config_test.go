package config

import (
	"strings"
	"testing"
)

// Production deployments MUST fail fast when required secrets are missing.
// The Validate method is the backstop for misconfigured prod rollouts.

func TestValidate_NoErrorInDev(t *testing.T) {
	c := &Config{Environment: "dev"}
	if err := c.Validate(); err != nil {
		t.Errorf("dev should always pass Validate, got %v", err)
	}
}

func TestValidate_NoErrorInStaging(t *testing.T) {
	c := &Config{Environment: "staging"}
	if err := c.Validate(); err != nil {
		t.Errorf("staging should pass Validate, got %v", err)
	}
}

func TestValidate_ProdRequiresPublicURL(t *testing.T) {
	c := &Config{Environment: "prod", KMSProvider: "vault"} // LocalKEK not required
	err := c.Validate()
	if err == nil {
		t.Fatal("prod without PublicURL should fail")
	}
	if !strings.Contains(err.Error(), "VAULTDMS_PUBLIC_URL") {
		t.Errorf("error should mention the missing var: %v", err)
	}
}

func TestValidate_ProdRequiresLocalKEKWhenKMSLocal(t *testing.T) {
	c := &Config{Environment: "prod", PublicURL: "https://x", KMSProvider: "local", LocalKEK: ""}
	err := c.Validate()
	if err == nil {
		t.Fatal("prod + kms_provider=local + empty LocalKEK must fail")
	}
	if !strings.Contains(err.Error(), "VAULTDMS_LOCAL_KEK") {
		t.Errorf("error should mention the missing var: %v", err)
	}
}

func TestValidate_ProdDoesNotRequireLocalKEKWhenKMSIsExternal(t *testing.T) {
	c := &Config{Environment: "prod", PublicURL: "https://x", KMSProvider: "vault", LocalKEK: ""}
	if err := c.Validate(); err != nil {
		t.Errorf("vault KMS shouldn't need LocalKEK: %v", err)
	}
}

func TestValidate_ProdListsAllMissingVars(t *testing.T) {
	c := &Config{Environment: "prod", KMSProvider: "local"}
	err := c.Validate()
	if err == nil {
		t.Fatal("want failure")
	}
	// Both PublicURL and LocalKEK missing → error string lists both.
	if !strings.Contains(err.Error(), "VAULTDMS_PUBLIC_URL") {
		t.Errorf("missing PublicURL not reported: %v", err)
	}
	if !strings.Contains(err.Error(), "VAULTDMS_LOCAL_KEK") {
		t.Errorf("missing LocalKEK not reported: %v", err)
	}
}

func TestRequireSecret_NoopInDev(t *testing.T) {
	c := &Config{Environment: "dev"}
	if err := c.RequireSecret("STRIPE_WEBHOOK_SECRET", ""); err != nil {
		t.Errorf("dev should accept empty secret: %v", err)
	}
}

func TestRequireSecret_RejectsEmptyInProd(t *testing.T) {
	c := &Config{Environment: "prod"}
	err := c.RequireSecret("STRIPE_WEBHOOK_SECRET", "")
	if err == nil {
		t.Fatal("prod + empty secret must fail")
	}
	if !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") {
		t.Errorf("error should name the secret: %v", err)
	}
}

func TestRequireSecret_AcceptsNonEmptyInProd(t *testing.T) {
	c := &Config{Environment: "prod"}
	if err := c.RequireSecret("X", "val"); err != nil {
		t.Errorf("non-empty should pass: %v", err)
	}
}

// ---- Load ----------------------------------------------------------------

func TestLoad_DefaultsApplied(t *testing.T) {
	// Clear any VAULTDMS_ env noise via the subtest.
	t.Setenv("VAULTDMS_DATABASE_URL", "postgres://u:p@h:1/d")
	t.Setenv("VAULTDMS_REDIS_URL", "localhost:6379")
	t.Setenv("VAULTDMS_NATS_URL", "nats://localhost:4222")

	cfg, err := Load("test-service")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ServiceName != "test-service" {
		t.Errorf("service name default: %q", cfg.ServiceName)
	}
	if cfg.Environment != "dev" {
		t.Errorf("default environment: %q, want dev", cfg.Environment)
	}
	if cfg.KMSProvider != "local" {
		t.Errorf("default KMS: %q, want local", cfg.KMSProvider)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("default http port: %d", cfg.HTTPPort)
	}
	if cfg.DatabaseURL != "postgres://u:p@h:1/d" {
		t.Errorf("DATABASE_URL not bound from env: %q", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "localhost:6379" {
		t.Errorf("REDIS_URL not bound: %q", cfg.RedisURL)
	}
	if cfg.NATSURL != "nats://localhost:4222" {
		t.Errorf("NATS_URL not bound: %q", cfg.NATSURL)
	}
}

func TestLoad_RejectsMissingRequired(t *testing.T) {
	// No VAULTDMS_DATABASE_URL set — validator must reject.
	t.Setenv("VAULTDMS_DATABASE_URL", "")
	t.Setenv("VAULTDMS_REDIS_URL", "localhost:6379")
	t.Setenv("VAULTDMS_NATS_URL", "nats://localhost:4222")
	if _, err := Load("svc"); err == nil {
		t.Fatal("missing DATABASE_URL should fail Load's validator")
	}
}

func TestLoad_NonVaultdmsPrefixesBind(t *testing.T) {
	t.Setenv("VAULTDMS_DATABASE_URL", "postgres://u:p@h:1/d")
	t.Setenv("VAULTDMS_REDIS_URL", "r")
	t.Setenv("VAULTDMS_NATS_URL", "n")
	t.Setenv("POLICY_SERVICE_ADDR", "my-policy:9000")
	t.Setenv("OPENSEARCH_URL", "http://my-os:9200")

	cfg, err := Load("svc")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PolicyServiceAddr != "my-policy:9000" {
		t.Errorf("POLICY_SERVICE_ADDR: %q", cfg.PolicyServiceAddr)
	}
	if cfg.OpenSearchURL != "http://my-os:9200" {
		t.Errorf("OPENSEARCH_URL: %q", cfg.OpenSearchURL)
	}
}
