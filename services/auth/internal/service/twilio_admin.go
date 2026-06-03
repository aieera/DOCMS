// Admin-side write path for tenant_twilio_configs. The read path
// lives in mfa_tenant_config.go (smsSenderForTenant). Saving sealed
// auth_token via the per-tenant KEK.
package service

import (
	"context"
	"errors"
	"time"

	"github.com/aieera/sedoc/pkg/esign"
	"github.com/aieera/sedoc/services/auth/internal/repository"
)

// TwilioConfigPublic is the secret-stripped view returned to the
// admin UI. AuthToken never travels back from the server; the modal
// shows "stored" as a presence flag and requires re-entry to change.
type TwilioConfigPublic struct {
	AccountSID       string    `json:"account_sid"`
	HasAuthToken     bool      `json:"has_auth_token"`
	VerifyServiceSID string    `json:"verify_service_sid"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// GetTwilioConfigForAdmin returns the (secret-stripped) saved Twilio
// config for the tenant, or nil if none exists.
func (s *Service) GetTwilioConfigForAdmin(ctx context.Context, tenantID string) (*TwilioConfigPublic, error) {
	if s.notifRepo == nil {
		return nil, errors.New("notification repo not wired")
	}
	row, err := s.notifRepo.GetTwilioConfig(ctx, tenantID)
	if err != nil || row == nil {
		return nil, err
	}
	return &TwilioConfigPublic{
		AccountSID:       row.AccountSID,
		HasAuthToken:     row.AuthTokenSealed != "",
		VerifyServiceSID: row.VerifyServiceSID,
		UpdatedAt:        row.UpdatedAt,
	}, nil
}

// SaveTwilioConfig persists per-tenant Twilio credentials. Seals
// auth_token with the local KEK before write.
func (s *Service) SaveTwilioConfig(ctx context.Context, tenantID, userID, accountSID, authToken, verifySID string) error {
	if s.notifRepo == nil {
		return errors.New("notification repo not wired")
	}
	if accountSID == "" || verifySID == "" {
		return errors.New("account_sid and verify_service_sid are required")
	}
	// On edit-without-secret, keep the existing sealed token rather
	// than requiring the admin to re-paste it on every config tweak.
	var sealed string
	if authToken == "" {
		existing, err := s.notifRepo.GetTwilioConfig(ctx, tenantID)
		if err != nil {
			return err
		}
		if existing == nil || existing.AuthTokenSealed == "" {
			return errors.New("auth_token is required for first save")
		}
		sealed = existing.AuthTokenSealed
	} else {
		var err error
		sealed, err = esign.SealString([]byte(authToken), s.localKek)
		if err != nil {
			return err
		}
	}
	return s.notifRepo.UpsertTwilioConfig(ctx, &repository.TwilioConfig{
		TenantID:         tenantID,
		AccountSID:       accountSID,
		AuthTokenSealed:  sealed,
		VerifyServiceSID: verifySID,
		UpdatedBy:        userID,
	})
}

func (s *Service) DeleteTwilioConfig(ctx context.Context, tenantID string) error {
	if s.notifRepo == nil {
		return errors.New("notification repo not wired")
	}
	return s.notifRepo.DeleteTwilioConfig(ctx, tenantID)
}

// TestTwilioSend fires a real Verify start against the saved tenant
// row to the given phone. Returns the Twilio error verbatim on failure
// so the admin sees exactly what went wrong.
func (s *Service) TestTwilioSend(ctx context.Context, tenantID, phoneE164 string) error {
	sender, err := s.smsSenderForTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	// Even if smsSenderForTenant fell back to s.mfa.SMS (env), that's
	// fine — the admin just wants to know SMS works from this deploy.
	if !sender.IsConfigured() && sender != s.mfa.SMS {
		return errors.New("twilio not configured for this tenant; save credentials first")
	}
	return sender.StartVerification(ctx, phoneE164)
}
