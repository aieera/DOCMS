// ADR 0070 — WebAuthn flow handlers.
//
// The four service methods Begin/FinishPasskeyRegistration and
// Begin/FinishPasskeyLogin wrap go-webauthn/webauthn against our
// existing UserRepository (uuid.UUID + pgx.Tx pattern) and the
// new WebAuthnRepository.
//
// Wiring contract: Service.WebAuthnLib holds a *webauthn.WebAuthn
// or nil. cmd/server constructs it once at startup from
// LoadWebAuthnConfigFromEnv(); nil means handlers return
// ErrWebAuthnNotImplemented.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	libwebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// NewWebAuthnLib constructs the lib instance from a WebAuthnConfig.
// Returns nil on a nil config so callers can do
//   svc.WebAuthnLib = service.NewWebAuthnLib(cfg)
// in cmd/server without a separate nil branch.
func NewWebAuthnLib(cfg *WebAuthnConfig) (*libwebauthn.WebAuthn, error) {
	if cfg == nil {
		return nil, nil
	}
	return libwebauthn.New(&libwebauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
	})
}

// webauthnLib returns the lib handle or an error when the deploy
// hasn't configured it. Used by every flow method to gate cleanly.
func (s *Service) webauthnLib() (*libwebauthn.WebAuthn, error) {
	if s.WebAuthnLib == nil {
		return nil, ErrWebAuthnNotImplemented
	}
	wa, ok := s.WebAuthnLib.(*libwebauthn.WebAuthn)
	if !ok {
		return nil, fmt.Errorf("webauthn: WebAuthnLib field is %T, want *libwebauthn.WebAuthn", s.WebAuthnLib)
	}
	return wa, nil
}

// ---- webauthn.User adapter ----------------------------------------

// libUser wraps a model.User + persisted creds into the
// libwebauthn.User interface.
type libUser struct {
	id    []byte
	name  string
	dn    string
	creds []libwebauthn.Credential
}

func (w *libUser) WebAuthnID() []byte                            { return w.id }
func (w *libUser) WebAuthnName() string                          { return w.name }
func (w *libUser) WebAuthnDisplayName() string                   { return w.dn }
func (w *libUser) WebAuthnCredentials() []libwebauthn.Credential { return w.creds }

func buildLibUser(u *model.User, creds []*model.WebAuthnCredential) *libUser {
	bin, _ := u.ID.MarshalBinary()
	libCreds := make([]libwebauthn.Credential, 0, len(creds))
	for _, c := range creds {
		libCreds = append(libCreds, libwebauthn.Credential{
			ID:        c.CredentialID,
			PublicKey: c.PublicKey,
			Authenticator: libwebauthn.Authenticator{
				AAGUID:    c.AAGUID,
				SignCount: uint32(c.SignCount), //nolint:gosec  // counter wraps; risk acknowledged in ADR
			},
			Transport: stringsToTransports(c.Transports),
			Flags: libwebauthn.CredentialFlags{
				BackupEligible: c.BackupEligible,
				BackupState:    c.BackupState,
			},
		})
	}
	return &libUser{
		id:    bin,
		name:  u.Email,
		dn:    u.DisplayName,
		creds: libCreds,
	}
}

func stringsToTransports(in []string) []protocol.AuthenticatorTransport {
	out := make([]protocol.AuthenticatorTransport, 0, len(in))
	for _, s := range in {
		out = append(out, protocol.AuthenticatorTransport(s))
	}
	return out
}

func transportsToStrings(in []protocol.AuthenticatorTransport) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, string(t))
	}
	return out
}

// ---- Registration --------------------------------------------------

// PasskeyRegistrationStart returns the credential-creation options
// + a session token the client echoes back to FinishRegistration.
// The caller must already be authenticated (handler enforces).
func (s *Service) PasskeyRegistrationStart(ctx context.Context, tenantID, userID uuid.UUID) (*protocol.CredentialCreation, string, error) {
	wa, err := s.webauthnLib()
	if err != nil {
		return nil, "", err
	}

	var (
		user  *model.User
		creds []*model.WebAuthnCredential
	)
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		user = u
		c, err := s.webauthn.ListCredentials(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		creds = c
		return nil
	})
	if err != nil {
		return nil, "", err
	}

	wu := buildLibUser(user, creds)

	// Exclude already-registered creds so the browser doesn't offer
	// a passkey the user has already saved.
	excludeList := make([]protocol.CredentialDescriptor, 0, len(creds))
	for _, c := range creds {
		excludeList = append(excludeList, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: c.CredentialID,
		})
	}

	options, sessionData, err := wa.BeginRegistration(wu, libwebauthn.WithExclusions(excludeList))
	if err != nil {
		return nil, "", fmt.Errorf("begin registration: %w", err)
	}

	sessionJSON, err := json.Marshal(sessionData)
	if err != nil {
		return nil, "", err
	}
	token, err := WrapSession(SessionEnvelope{
		UserID:   userID.String(),
		Flow:     "register",
		Session:  sessionJSON,
		IssuedAt: time.Now().Unix(),
	})
	if err != nil {
		return nil, "", err
	}
	return options, token, nil
}

// PasskeyRegistrationFinish validates the attestation, persists the
// credential, and returns the new row.
func (s *Service) PasskeyRegistrationFinish(
	ctx context.Context,
	tenantID uuid.UUID,
	sessionToken, friendlyName string,
	attestationJSON []byte,
) (*model.WebAuthnCredential, error) {
	wa, err := s.webauthnLib()
	if err != nil {
		return nil, err
	}
	if friendlyName == "" {
		return nil, errors.New("friendly_name required (e.g. \"Work laptop\")")
	}

	env, err := UnwrapSession(sessionToken, "register")
	if err != nil {
		return nil, err
	}
	envUserID, err := uuid.Parse(env.UserID)
	if err != nil {
		return nil, ErrInvalidSession
	}

	var sessionData libwebauthn.SessionData
	if err := json.Unmarshal(env.Session, &sessionData); err != nil {
		return nil, ErrInvalidSession
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(attestationJSON))
	if err != nil {
		return nil, fmt.Errorf("parse attestation: %w", err)
	}

	var row *model.WebAuthnCredential
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		user, err := s.users.GetByID(ctx, tx, tenantID, envUserID)
		if err != nil {
			return err
		}
		creds, err := s.webauthn.ListCredentials(ctx, tx, tenantID, envUserID)
		if err != nil {
			return err
		}
		wu := buildLibUser(user, creds)
		libCred, err := wa.CreateCredential(wu, sessionData, parsed)
		if err != nil {
			return fmt.Errorf("verify attestation: %w", err)
		}
		row = &model.WebAuthnCredential{
			TenantID:       tenantID,
			UserID:         envUserID,
			CredentialID:   libCred.ID,
			PublicKey:      libCred.PublicKey,
			SignCount:      int64(libCred.Authenticator.SignCount),
			AAGUID:         libCred.Authenticator.AAGUID,
			Transports:     transportsToStrings(libCred.Transport),
			Name:           friendlyName,
			BackupEligible: libCred.Flags.BackupEligible,
			BackupState:    libCred.Flags.BackupState,
		}
		return s.webauthn.UpsertCredential(ctx, tx, row)
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ---- Login (assertion) ---------------------------------------------

// PasskeyLoginStart resolves the user by tenant_slug + email,
// produces the assertion options + session token. The user does not
// have to have logged in already (passwordless passkey path).
func (s *Service) PasskeyLoginStart(ctx context.Context, tenantSlug, email string) (*protocol.CredentialAssertion, string, error) {
	wa, err := s.webauthnLib()
	if err != nil {
		return nil, "", err
	}
	emailNorm, err := validateEmail(email)
	if err != nil {
		return nil, "", asInvalidCredentials(err)
	}
	org, err := s.users.FindOrganizationBySlug(ctx, s.pool, tenantSlug)
	if err != nil || org == nil {
		return nil, "", ErrInvalidCredentials
	}

	var (
		user  *model.User
		creds []*model.WebAuthnCredential
	)
	err = database.WithTenantTx(ctx, s.pool, org.ID, func(tx pgx.Tx) error {
		u, err := s.users.GetByEmail(ctx, tx, org.ID, emailNorm)
		if err != nil {
			return err
		}
		user = u
		c, err := s.webauthn.ListCredentials(ctx, tx, org.ID, u.ID)
		if err != nil {
			return err
		}
		creds = c
		return nil
	})
	if err != nil {
		// Don't leak NotFound as 4xx with "no such user" — same
		// shape as password login (ErrInvalidCredentials).
		return nil, "", ErrInvalidCredentials
	}
	if len(creds) == 0 {
		// Typed sentinel so the handler returns 404 with a
		// "register one in Settings → Security" hint, NOT a
		// generic 500 from errors.New.
		return nil, "", ErrNoPasskeysRegistered
	}

	wu := buildLibUser(user, creds)
	options, sessionData, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, "", fmt.Errorf("begin login: %w", err)
	}

	sessionJSON, err := json.Marshal(sessionData)
	if err != nil {
		return nil, "", err
	}
	// Stash tenant_id alongside user_id in the session envelope so
	// finish doesn't need to look up the org again.
	env := SessionEnvelope{
		UserID:   user.ID.String() + "|" + user.TenantID.String(),
		Flow:     "login",
		Session:  sessionJSON,
		IssuedAt: time.Now().Unix(),
	}
	token, err := WrapSession(env)
	if err != nil {
		return nil, "", err
	}
	return options, token, nil
}

// PasskeyLoginFinish validates the assertion, bumps sign_count +
// last_used_at, issues a step_up_grant + session, and returns
// CreatedSession the same shape password login does so the
// handler can mint the cookie identically.
func (s *Service) PasskeyLoginFinish(
	ctx context.Context,
	sessionToken string,
	assertionJSON []byte,
	ip, ua string,
) (*CreatedSession, error) {
	wa, err := s.webauthnLib()
	if err != nil {
		return nil, err
	}
	env, err := UnwrapSession(sessionToken, "login")
	if err != nil {
		return nil, err
	}
	userID, tenantID, err := splitUserTenant(env.UserID)
	if err != nil {
		return nil, ErrInvalidSession
	}

	var sessionData libwebauthn.SessionData
	if err := json.Unmarshal(env.Session, &sessionData); err != nil {
		return nil, ErrInvalidSession
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(assertionJSON))
	if err != nil {
		return nil, fmt.Errorf("parse assertion: %w", err)
	}

	var (
		user *model.User
	)
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		user = u
		creds, err := s.webauthn.ListCredentials(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		wu := buildLibUser(user, creds)
		libCred, err := wa.ValidateLogin(wu, sessionData, parsed)
		if err != nil {
			return fmt.Errorf("verify assertion: %w", err)
		}
		// Backward-rolling sign_count = cloned authenticator.
		// (The lib already raises CredentialClonedError; this is
		// belt-and-suspenders for the legacy 0-counter case some
		// authenticators report.)
		for _, existing := range creds {
			if bytesEq(existing.CredentialID, libCred.ID) &&
				libCred.Authenticator.SignCount != 0 &&
				uint32(existing.SignCount) > libCred.Authenticator.SignCount {
				return fmt.Errorf("sign_count rolled backward (existing=%d, new=%d) — possible clone",
					existing.SignCount, libCred.Authenticator.SignCount)
			}
		}
		// Persist sign_count + last_used_at, then insert step-up grant.
		if err := s.webauthn.RecordUse(ctx, tx, tenantID, libCred.ID, int64(libCred.Authenticator.SignCount)); err != nil {
			return err
		}
		return s.webauthn.InsertStepUpGrant(ctx, tx, &model.StepUpGrant{
			TenantID:     tenantID,
			UserID:       userID,
			Scope:        "",
			GrantedVia:   "webauthn",
			CredentialID: libCred.ID,
			ExpiresAt:    time.Now().Add(StepUpTTL),
		})
	})
	if err != nil {
		return nil, err
	}

	return s.finishLogin(ctx, user, "webauthn", ip, ua)
}

// ---- List + Delete --------------------------------------------------

// ListPasskeys for /settings/security.
func (s *Service) ListPasskeys(ctx context.Context, tenantID, userID uuid.UUID) ([]*model.WebAuthnCredential, error) {
	if s.webauthn == nil {
		return nil, ErrWebAuthnNotImplemented
	}
	var out []*model.WebAuthnCredential
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		c, err := s.webauthn.ListCredentials(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		out = c
		return nil
	})
	return out, err
}

// DeletePasskey removes a cred. Tenant + user predicate enforced
// at the SQL layer.
func (s *Service) DeletePasskey(ctx context.Context, tenantID, userID uuid.UUID, credentialID []byte) error {
	if s.webauthn == nil {
		return ErrWebAuthnNotImplemented
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.webauthn.DeleteCredential(ctx, tx, tenantID, userID, credentialID)
	})
}

// HasActiveStepUp surfaces step_up_grants membership for the
// step-up middleware (commit 4 wires this).
func (s *Service) HasActiveStepUp(ctx context.Context, tenantID, userID uuid.UUID, scope string) (bool, error) {
	if s.webauthn == nil {
		return false, nil
	}
	var ok bool
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		v, err := s.webauthn.HasActiveStepUp(ctx, tx, tenantID, userID, scope)
		if err != nil {
			return err
		}
		ok = v
		return nil
	})
	return ok, err
}

// ---- helpers --------------------------------------------------------

// splitUserTenant decodes the "user|tenant" composite the login
// envelope uses. Returns (user, tenant, err).
func splitUserTenant(s string) (uuid.UUID, uuid.UUID, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			u, err := uuid.Parse(s[:i])
			if err != nil {
				return uuid.Nil, uuid.Nil, err
			}
			t, err := uuid.Parse(s[i+1:])
			if err != nil {
				return uuid.Nil, uuid.Nil, err
			}
			return u, t, nil
		}
	}
	return uuid.Nil, uuid.Nil, errors.New("invalid user|tenant string")
}

func bytesEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
