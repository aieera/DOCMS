package service

// D-13 follow-up — pkg/config defaults Environment to "dev" when
// SEDOC_ENVIRONMENT is UNSET, so feeding cfg.Environment to ResolveSecret
// let every install that forgot the variable run on the published dev
// constants. The dev fallback now keys on the raw variable (trimmed), like
// pkg/license: only an explicit SEDOC_ENVIRONMENT=dev is dev.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
)

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "") // registers restore
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestResolveSecret_UnsetEnvironmentIsNotDev(t *testing.T) {
	const devDefault = "dev-default"

	unsetEnvForTest(t, "SEDOC_ENVIRONMENT")
	if got := RawEnvironment(); got != "" {
		t.Fatalf("RawEnvironment with the variable unset = %q, want empty", got)
	}
	if s, err := ResolveSecret(RawEnvironment(), "", devDefault); !errors.Is(err, ErrSecretNotConfigured) || s != nil {
		t.Fatalf("unset SEDOC_ENVIRONMENT must not unlock the dev fallback: %q %v", s, err)
	}
	if s, err := ResolveSecret(RawEnvironment(), devDefault, devDefault); !errors.Is(err, ErrSecretNotConfigured) || s != nil {
		t.Fatalf("unset SEDOC_ENVIRONMENT must refuse the dev value: %q %v", s, err)
	}

	t.Setenv("SEDOC_ENVIRONMENT", "  dev ")
	if s, err := ResolveSecret(RawEnvironment(), "", devDefault); err != nil || string(s) != devDefault {
		t.Fatalf("explicit dev keeps the fallback: %q %v", s, err)
	}

	// IRM: the DocumentService environment is cfg.Environment, which is
	// "dev" by default — it must not unlock the compiled-in signing key.
	unsetEnvForTest(t, "SEDOC_ENVIRONMENT")
	t.Setenv("SEDOC_IRM_SIGNING_KEY", devIRMSigningKey)
	doc := New(nil, nil, nil, zerolog.Nop())
	doc.SetEnvironment("dev") // pkg/config's default for an unset variable
	irm := NewIRMService(nil, nil, nil, nil, doc, zerolog.Nop())
	tenant, user := uuid.New(), uuid.New()
	ctx := auth.WithUser(auth.SetTenantID(context.Background(), tenant), auth.UserInfo{ID: user, TenantID: tenant})
	_, err := irm.ExportProtected(ctx, IRMExportInput{
		DocumentID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour),
		Recipients: []IRMRecipientInput{{Type: "email", Ref: "x@example.com"}},
	})
	if !errors.Is(err, ErrSecretNotConfigured) {
		t.Fatalf("IRM with unset SEDOC_ENVIRONMENT and the dev key: want ErrSecretNotConfigured, got %v", err)
	}
}
