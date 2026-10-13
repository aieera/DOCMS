package service

// D-13 — deployment secrets with hard-coded dev fallbacks.
//
// The IRM policy-header signing key silently fell back to a constant
// compiled into the binary when SEDOC_IRM_SIGNING_KEY was unset, in every
// environment: a production deployment that forgot the env var signed
// every protected container with a key anyone can read in the source.
// Outside dev the key is now required, and the export fails closed (the
// handler maps it to 503) before any document is read or sealed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
)

func TestIRMSigningKey_RequiredOutsideDev(t *testing.T) {
	t.Run("ResolveSecret", func(t *testing.T) {
		const devDefault = "dev-default"
		for _, tc := range []struct {
			env, configured string
			want            string
			wantErr         bool
		}{
			{"dev", "", devDefault, false},
			{"dev", "real", "real", false},
			{"dev", devDefault, devDefault, false},
			{"staging", "", "", true},
			{"prod", "", "", true},
			{"", "", "", true}, // unknown environment is not dev
			{"prod", devDefault, "", true},
			{"staging", devDefault, "", true},
			{"prod", "real", "real", false},
		} {
			got, err := ResolveSecret(tc.env, tc.configured, devDefault)
			if tc.wantErr {
				if !errors.Is(err, ErrSecretNotConfigured) || got != nil {
					t.Fatalf("env=%q configured=%q: want ErrSecretNotConfigured, got %q, %v", tc.env, tc.configured, got, err)
				}
				continue
			}
			if err != nil || string(got) != tc.want {
				t.Fatalf("env=%q configured=%q: want %q, got %q, %v", tc.env, tc.configured, tc.want, got, err)
			}
		}
	})

	t.Run("export fails closed outside dev", func(t *testing.T) {
		t.Setenv("SEDOC_IRM_SIGNING_KEY", "")
		tenant, user := uuid.New(), uuid.New()
		ctx := auth.WithUser(auth.SetTenantID(context.Background(), tenant),
			auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})
		in := IRMExportInput{
			DocumentID: uuid.New(),
			Recipients: []IRMRecipientInput{{Type: "email", Ref: "x@example.com"}},
			ExpiresAt:  time.Now().Add(time.Hour),
		}
		for _, env := range []string{"staging", "prod"} {
			doc := New(nil, nil, nil, zerolog.Nop())
			doc.SetEnvironment(env)
			irm := NewIRMService(nil, nil, nil, nil, doc, zerolog.Nop())
			// nil pool/repos/s3: reaching any of them would panic, so a
			// clean ErrSecretNotConfigured proves the gate runs first.
			if _, err := irm.ExportProtected(ctx, in); !errors.Is(err, ErrSecretNotConfigured) {
				t.Fatalf("env=%s: want ErrSecretNotConfigured, got %v", env, err)
			}
		}
	})

	t.Run("configured key is used", func(t *testing.T) {
		t.Setenv("SEDOC_IRM_SIGNING_KEY", "prod-irm-key")
		doc := New(nil, nil, nil, zerolog.Nop())
		doc.SetEnvironment("prod")
		irm := NewIRMService(nil, nil, nil, nil, doc, zerolog.Nop())
		if string(irm.signingKey) != "prod-irm-key" {
			t.Fatalf("signing key = %q", irm.signingKey)
		}
	})
}
