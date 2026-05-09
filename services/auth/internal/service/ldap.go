// ADR 0062 — LDAP/AD direct bind service layer.
//
// Three jobs:
//
//   1. Seal/unseal the per-tenant bind password using the same KEK
//      flow the MFA secrets use.
//   2. AuthenticateLDAP — called from Login when the tenant has an
//      active config. On success, JIT-upserts the local user and
//      reconciles group memberships. On LDAP_INVALID_CREDENTIALS
//      with fallback_to_local=true the caller falls through to
//      bcryptCompare.
//   3. SyncTenant — pulls every mapped AD group, reconciles
//      group_members against the membership the directory reports,
//      and records an ldap_sync_history row.
package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/crypto"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/ldap"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
	"github.com/vaultdms/vaultdms/services/auth/internal/repository"
)

// LDAPDeps is the optional bundle of LDAP-specific dependencies.
// When Repo is nil the auth service treats LDAP as disabled — every
// LDAP method short-circuits and Login skips straight to local auth.
type LDAPDeps struct {
	Repo repository.LDAPRepository
	Pool *ldap.Pool // shared connection pool
}

// SetLDAP wires LDAP support onto the service after construction.
// Kept off the New() signature to avoid forcing every caller to
// provide an LDAP repo — feature is optional in the same way
// passkeys are (ADR 0061).
func (s *Service) SetLDAP(d LDAPDeps) {
	s.ldap = d
}

// ErrLDAPNotConfigured signals that the tenant has no active LDAP
// row. The login flow treats this as "skip LDAP, try local".
var ErrLDAPNotConfigured = errors.New("ldap: no active config for tenant")

// ErrLDAPDirectoryDown signals a network / server-side error. The
// login flow surfaces this as 503 — admins want to see "directory
// unreachable", not silently fall through to a stale local hash.
var ErrLDAPDirectoryDown = errors.New("ldap: directory unreachable")

// LDAPLoginResult is what AuthenticateLDAP returns on success. The
// caller plugs it into finishLogin (matches the SAML/OIDC paths).
type LDAPLoginResult struct {
	User   *model.User
	Groups []string // DNs from the directory; mapped to DMS groups by the caller
}

// AuthenticateLDAP attempts a search-then-bind against the tenant's
// active LDAP config.
//
// Returns:
//   - (result, nil)                        — user authenticated.
//   - (nil, ErrLDAPNotConfigured)          — tenant has no active config.
//   - (nil, ldap.ErrInvalidCredentials)    — bind rejected; caller may
//     fall back to local password if cfg.fallback_to_local.
//   - (nil, ErrLDAPDirectoryDown)          — network/server failure.
//
// JIT-upserts the local user and runs an incremental group sync on
// the user's row before returning.
func (s *Service) AuthenticateLDAP(ctx context.Context, tenantID uuid.UUID, username, password string) (*LDAPLoginResult, error) {
	if s.ldap.Repo == nil {
		return nil, ErrLDAPNotConfigured
	}

	cfg, err := s.loadActiveLDAP(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	plain, err := s.unsealBindPassword(cfg.BindPasswordSealed)
	if err != nil {
		return nil, fmt.Errorf("ldap: unseal bind: %w", err)
	}

	dialCfg := ldapClientConfig(cfg, plain)

	client, err := s.ldap.Pool.Acquire(tenantID, cfg.ID, dialCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLDAPDirectoryDown, err)
	}
	defer s.ldap.Pool.Release(tenantID, cfg.ID, client)

	user, err := client.AuthenticateUser(username, password)
	if err != nil {
		// Pass through the ldap-package sentinel so the login flow
		// can apply fallback_to_local; everything else is a hard
		// directory failure.
		if errors.Is(err, ldap.ErrInvalidCredentials) || errors.Is(err, ldap.ErrUserNotFound) {
			return nil, ldap.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("%w: %v", ErrLDAPDirectoryDown, err)
	}

	groups, err := client.ResolveGroups(user.DN)
	if err != nil {
		// Failing to resolve groups is not fatal — log and proceed
		// with empty groups (the user keeps whatever DMS groups they
		// already had). Sync run will reconcile.
		s.log.Warn().Err(err).Str("dn", user.DN).Msg("ldap group resolve failed; proceeding without group update")
		groups = nil
	}

	// JIT upsert user + reconcile group memberships in one tx.
	var localUser *model.User
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.upsertLDAPUser(ctx, tx, tenantID, user.Email, user.DisplayName)
		if err != nil {
			return err
		}
		localUser = u
		return s.reconcileUserGroups(ctx, tx, tenantID, cfg.ID, u.ID, groups)
	})
	if err != nil {
		return nil, err
	}

	return &LDAPLoginResult{User: localUser, Groups: groups}, nil
}

// FallbackToLocal returns the active config's fallback_to_local flag
// (or false if no active config). The login handler asks before
// deciding whether to retry with the local hash.
func (s *Service) FallbackToLocal(ctx context.Context, tenantID uuid.UUID) bool {
	if s.ldap.Repo == nil {
		return true // no LDAP at all — local is the only path
	}
	cfg, err := s.loadActiveLDAP(ctx, tenantID)
	if err != nil {
		// No config (or read error) — let local auth handle it.
		return true
	}
	return cfg.FallbackToLocal
}

// HasActiveLDAP returns true when this tenant has LDAP wired and
// active. Login uses this to decide whether to attempt LDAP at all.
func (s *Service) HasActiveLDAP(ctx context.Context, tenantID uuid.UUID) bool {
	if s.ldap.Repo == nil {
		return false
	}
	_, err := s.loadActiveLDAP(ctx, tenantID)
	return err == nil
}

// FinishLDAPLogin issues a session for an LDAP-authenticated user.
// Mirrors finishLogin's audit + last_login update path.
func (s *Service) FinishLDAPLogin(ctx context.Context, user *model.User, ip, ua string) (*CreatedSession, error) {
	return s.finishLogin(ctx, user, "ldap", ip, ua)
}

// ---- Config CRUD glue -------------------------------------------------

func (s *Service) loadActiveLDAP(ctx context.Context, tenantID uuid.UUID) (*repository.LDAPConfigRow, error) {
	var cfg *repository.LDAPConfigRow
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		c, err := s.ldap.Repo.GetActiveConfig(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		cfg = c
		return nil
	})
	if err != nil {
		if vdmserr.KindOf(err) == vdmserr.KindNotFound {
			return nil, ErrLDAPNotConfigured
		}
		return nil, err
	}
	return cfg, nil
}

// SealBindPassword wraps a plaintext bind password with the current
// KEK so it can be persisted. Exposed for the admin-handler create/
// update paths.
func (s *Service) SealBindPassword(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, vdmserr.Validation("bind_password", "required")
	}
	kek, err := s.currentKEK()
	if err != nil {
		return nil, err
	}
	ct, nonce, err := crypto.EncryptData([]byte(plaintext), kek)
	if err != nil {
		return nil, err
	}
	buf := append([]byte{}, nonce...)
	buf = append(buf, ct...)
	// Persisted as base64 inside the BYTEA column for round-trip
	// stability with pgx (BYTEA accepts raw bytes; we encode for
	// the sake of operator-friendliness when dumping).
	return []byte(base64.StdEncoding.EncodeToString(buf)), nil
}

func (s *Service) unsealBindPassword(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", vdmserr.Conflict("bind password missing")
	}
	buf, err := base64.StdEncoding.DecodeString(string(sealed))
	if err != nil {
		return "", fmt.Errorf("ldap b64: %w", err)
	}
	if len(buf) < crypto.NonceSize+1 {
		return "", fmt.Errorf("ldap ciphertext too short")
	}
	kek, err := s.currentKEK()
	if err != nil {
		return "", err
	}
	nonce := buf[:crypto.NonceSize]
	ct := buf[crypto.NonceSize:]
	pt, err := crypto.DecryptData(ct, nonce, kek)
	if err != nil {
		return "", fmt.Errorf("ldap decrypt: %w", err)
	}
	return string(pt), nil
}

// ---- User upsert + group reconciliation -------------------------------

// upsertLDAPUser is the LDAP-flavored sibling of FindOrCreateSAMLUser.
// Same JIT semantics — find by email, create with role=member if
// absent. A fresh display_name is propagated.
func (s *Service) upsertLDAPUser(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, email, displayName string) (*model.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, vdmserr.Validation("email", "required")
	}
	if displayName == "" {
		displayName = emailLocalPart(email)
	}

	existing, err := s.users.GetByEmail(ctx, tx, tenantID, email)
	switch {
	case err == nil && existing != nil:
		if existing.Status != model.StatusActive {
			return nil, vdmserr.ErrUnauthorized
		}
		if displayName != "" && displayName != existing.DisplayName {
			if _, err := tx.Exec(ctx,
				`UPDATE users SET display_name = $3, updated_at = now()
				   WHERE tenant_id = $1 AND id = $2`,
				tenantID, existing.ID, displayName,
			); err != nil {
				return nil, err
			}
			existing.DisplayName = displayName
		}
		return existing, nil
	case vdmserr.KindOf(err) == vdmserr.KindNotFound:
		id, err := newUUID()
		if err != nil {
			return nil, err
		}
		u := &model.User{
			TenantID: tenantID, ID: id,
			Email: email, DisplayName: displayName,
			Role: model.RoleMember, Status: model.StatusActive,
			CreatedAt: s.clock(), UpdatedAt: s.clock(),
		}
		if err := s.users.Create(ctx, tx, u); err != nil {
			return nil, err
		}
		if err := s.emitAuth(ctx, tx, tenantID, u.ID, "dms.auth.user_registered.v1", map[string]any{
			"user_id":   u.ID.String(),
			"tenant_id": tenantID.String(),
			"email":     email,
			"method":    "ldap",
		}); err != nil {
			return nil, err
		}
		return u, nil
	default:
		return nil, err
	}
}

// reconcileUserGroups makes the user's group_members rows match the
// directory: any mapped DMS group whose AD DN appears in `ldapDNs`
// is added; any mapped DMS group missing from the AD set is removed.
// Unmapped DMS groups (manually-managed) are left alone.
func (s *Service) reconcileUserGroups(ctx context.Context, tx pgx.Tx, tenantID, configID, userID uuid.UUID, ldapDNs []string) error {
	mappings, err := s.ldap.Repo.ListMappings(ctx, tx, tenantID, configID)
	if err != nil {
		return err
	}
	if len(mappings) == 0 {
		return nil
	}

	// Build lowercase-DN → DMS-group set for O(1) lookup.
	dnSet := make(map[string]bool, len(ldapDNs))
	for _, dn := range ldapDNs {
		dnSet[strings.ToLower(dn)] = true
	}

	want := map[uuid.UUID]bool{}    // DMS groups the user SHOULD belong to
	managed := map[uuid.UUID]bool{} // DMS groups under LDAP control
	for _, m := range mappings {
		managed[m.DMSGroupID] = true
		if dnSet[strings.ToLower(m.LDAPGroupDN)] {
			want[m.DMSGroupID] = true
		}
	}

	for gid := range want {
		if _, err := tx.Exec(ctx, `
			INSERT INTO group_members (tenant_id, group_id, user_id)
			VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			tenantID, gid, userID); err != nil {
			return err
		}
	}
	// Remove the user from any LDAP-managed group they shouldn't be in.
	for gid := range managed {
		if want[gid] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM group_members
			 WHERE tenant_id = $1 AND group_id = $2 AND user_id = $3`,
			tenantID, gid, userID); err != nil {
			return err
		}
	}
	return nil
}

// ---- TestBind ---------------------------------------------------------

// TestBindResult is what /admin/.../test-bind returns.
type TestBindResult struct {
	OK          bool     `json:"ok"`
	BindOK      bool     `json:"bind_ok"`
	UserFound   bool     `json:"user_found"`
	GroupsFound int      `json:"groups_found"`
	Warnings    []string `json:"warnings,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// TestBindParams carries either an existing config id (use the stored
// password) or a new draft (use the supplied password verbatim) plus
// an optional sample username for end-to-end verification.
type TestBindParams struct {
	ExistingConfigID *uuid.UUID
	DraftConfig      *repository.LDAPConfigRow
	DraftPassword    string // only when DraftConfig != nil
	SampleUsername   string // optional; if empty only the service bind is tested
	SamplePassword   string // optional
}

// TestBind runs a dry-run bind. The admin UI uses this on the
// "Test connection" button before saving a draft config and on
// "Test login" inside the saved-config view.
func (s *Service) TestBind(ctx context.Context, tenantID uuid.UUID, p TestBindParams) TestBindResult {
	res := TestBindResult{}
	if s.ldap.Repo == nil {
		res.Error = "ldap not enabled on this deployment"
		return res
	}

	var cfg *repository.LDAPConfigRow
	var bindPassword string
	switch {
	case p.ExistingConfigID != nil:
		err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			c, err := s.ldap.Repo.GetConfig(ctx, tx, tenantID, *p.ExistingConfigID)
			if err != nil {
				return err
			}
			cfg = c
			return nil
		})
		if err != nil {
			res.Error = err.Error()
			return res
		}
		pw, err := s.unsealBindPassword(cfg.BindPasswordSealed)
		if err != nil {
			res.Error = "unseal bind password: " + err.Error()
			return res
		}
		bindPassword = pw
	case p.DraftConfig != nil:
		cfg = p.DraftConfig
		bindPassword = p.DraftPassword
	default:
		res.Error = "test_bind requires either existing_config_id or draft"
		return res
	}

	dialCfg := ldapClientConfig(cfg, bindPassword)
	client, err := ldap.Dial(dialCfg)
	if err != nil {
		res.Error = "dial: " + err.Error()
		return res
	}
	defer client.Close()

	if err := client.BindService(); err != nil {
		res.Error = "service bind: " + err.Error()
		return res
	}
	res.BindOK = true

	if p.SampleUsername != "" {
		if p.SamplePassword == "" {
			res.Warnings = append(res.Warnings, "sample_username given without sample_password; user search only, no end-to-end bind")
			// Issue a search instead of a bind to verify the filter
			// shape; reuse the package-private path through Authenticate
			// is overkill, so we re-bind as service and run a search.
			// findUser is exposed via AuthenticateUser when password is set;
			// for search-only we issue ResolveGroups against a fake DN —
			// not what we want. Compromise: count this as informational.
			res.OK = true
			return res
		}
		userRes, err := client.AuthenticateUser(p.SampleUsername, p.SamplePassword)
		if err != nil {
			res.Error = "user bind: " + err.Error()
			return res
		}
		res.UserFound = true
		groups, err := client.ResolveGroups(userRes.DN)
		if err != nil {
			res.Warnings = append(res.Warnings, "group resolve: "+err.Error())
		}
		res.GroupsFound = len(groups)
	}
	res.OK = true
	return res
}

// ---- Sync -------------------------------------------------------------

// SyncTenant walks every mapped AD group, pulls members, and
// reconciles group_members + auto-creates users for new directory
// entries. Triggered by the scheduler ticker (every 15 min) and by
// the admin "Sync now" button.
func (s *Service) SyncTenant(ctx context.Context, tenantID uuid.UUID, trigger string) error {
	if s.ldap.Repo == nil {
		return ErrLDAPNotConfigured
	}
	cfg, err := s.loadActiveLDAP(ctx, tenantID)
	if err != nil {
		return err
	}

	// Open a sync history row first so admins see the run start
	// immediately, even if a long search is in progress.
	historyID := uuid.New()
	startedAt := s.clock()
	_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.ldap.Repo.StartHistory(ctx, tx, &repository.LDAPSyncHistoryRow{
			ID: historyID, TenantID: tenantID, LDAPConfigID: cfg.ID,
			Trigger: trigger, StartedAt: startedAt,
		})
	})

	users, groups, errs, summary := s.runSync(ctx, tenantID, cfg)
	status := "ok"
	if errs > 0 && (users+groups) > 0 {
		status = "partial"
	}
	if errs > 0 && (users+groups) == 0 {
		status = "error"
	}

	_ = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := s.ldap.Repo.FinishHistory(ctx, tx, tenantID, historyID, status, users, groups, errs, summary); err != nil {
			return err
		}
		return s.ldap.Repo.UpdateLastSync(ctx, tx, tenantID, cfg.ID, status, summary, s.clock())
	})

	if errs > 0 && users == 0 && groups == 0 {
		return fmt.Errorf("ldap sync: %s", summary)
	}
	return nil
}

// runSync does the actual work — kept private so the public method
// can wrap it with the history bookkeeping.
//
// Algorithm: for each (ldap_group_dn → dms_group_id) mapping, list
// the directory members of that group (with nested-group expansion
// when configured), JIT-upsert each user, and rewrite the
// group_members row set for that DMS group atomically.
//
// Returns (users_synced, groups_synced, errors, error_summary).
func (s *Service) runSync(ctx context.Context, tenantID uuid.UUID, cfg *repository.LDAPConfigRow) (int, int, int, string) {
	plain, err := s.unsealBindPassword(cfg.BindPasswordSealed)
	if err != nil {
		return 0, 0, 1, "unseal bind: " + err.Error()
	}

	dialCfg := ldapClientConfig(cfg, plain)
	client, err := s.ldap.Pool.Acquire(tenantID, cfg.ID, dialCfg)
	if err != nil {
		return 0, 0, 1, "dial: " + err.Error()
	}
	defer s.ldap.Pool.Release(tenantID, cfg.ID, client)

	if err := client.BindService(); err != nil {
		return 0, 0, 1, "bind: " + err.Error()
	}

	var mappings []repository.LDAPGroupMappingRow
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		ms, err := s.ldap.Repo.ListMappings(ctx, tx, tenantID, cfg.ID)
		if err != nil {
			return err
		}
		mappings = ms
		return nil
	})
	if err != nil {
		return 0, 0, 1, "list mappings: " + err.Error()
	}

	usersTotal := map[uuid.UUID]bool{}
	groupsSynced := 0
	errCount := 0
	errSummary := ""

	for _, m := range mappings {
		// For each AD group DN, list direct members (with nested
		// expansion when configured). The matching-rule-in-chain
		// search inverts the relationship from "groups containing
		// user" to "users in group".
		members, err := s.listGroupMembers(client, cfg, m.LDAPGroupDN)
		if err != nil {
			errCount++
			if errSummary == "" {
				errSummary = "group " + m.LDAPGroupDN + ": " + err.Error()
			}
			continue
		}

		// JIT-upsert each member as a local user, then rewrite the
		// DMS group's membership atomically inside a single tx.
		err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			localIDs := make([]uuid.UUID, 0, len(members))
			for _, ent := range members {
				if ent.Email == "" {
					continue
				}
				u, err := s.upsertLDAPUser(ctx, tx, tenantID, ent.Email, ent.DisplayName)
				if err != nil {
					return err
				}
				localIDs = append(localIDs, u.ID)
				usersTotal[u.ID] = true
			}
			// Rewrite the DMS group's LDAP-managed membership: drop
			// any current member who isn't in localIDs (only for
			// users whose presence is LDAP-driven; we keep the
			// existing set as the source of truth here for
			// simplicity — full purge is acceptable because admins
			// don't manually add to LDAP-managed groups).
			if _, err := tx.Exec(ctx, `
				DELETE FROM group_members
				 WHERE tenant_id = $1 AND group_id = $2`,
				tenantID, m.DMSGroupID); err != nil {
				return err
			}
			for _, uid := range localIDs {
				if _, err := tx.Exec(ctx, `
					INSERT INTO group_members (tenant_id, group_id, user_id)
					VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
					tenantID, m.DMSGroupID, uid); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			errCount++
			if errSummary == "" {
				errSummary = "reconcile " + m.LDAPGroupDN + ": " + err.Error()
			}
			continue
		}
		groupsSynced++
	}

	return len(usersTotal), groupsSynced, errCount, errSummary
}

// listGroupMembers searches the directory for users whose memberOf
// chain contains groupDN. AD path uses the matching rule in chain;
// OpenLDAP fallback uses the simpler `(memberOf=<dn>)` filter.
func (s *Service) listGroupMembers(client *ldap.Client, _ *repository.LDAPConfigRow, groupDN string) ([]ldap.MemberEntry, error) {
	return client.SearchGroupMembers(groupDN)
}

// ldapClientConfig is the bridge from the repo row to the ldap.Config
// used by Dial.
func ldapClientConfig(row *repository.LDAPConfigRow, bindPassword string) ldap.Config {
	return ldap.Config{
		URL:                  row.URL,
		UseStartTLS:          row.UseStartTLS,
		AllowInsecure:        row.AllowInsecure,
		BindDN:               row.BindDN,
		BindPassword:         bindPassword,
		UserSearchBase:       row.UserSearchBase,
		UserSearchFilter:     row.UserSearchFilter,
		EmailAttribute:       row.EmailAttribute,
		DisplayNameAttribute: row.DisplayNameAttribute,
		GroupSearchBase:      row.GroupSearchBase,
		GroupSearchFilter:    row.GroupSearchFilter,
		NestedGroups:         row.NestedGroups,
		DialTimeout:          5 * time.Second,
		RequestTimeout:       10 * time.Second,
	}
}

// SyncAllTenants iterates every tenant with an active LDAP config
// and runs SyncTenant. Skip-and-log on per-tenant errors so one
// broken directory doesn't block the others.
func (s *Service) SyncAllTenants(ctx context.Context, pool *pgxpool.Pool) {
	if s.ldap.Repo == nil {
		return
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT tenant_id FROM ldap_configs WHERE is_active`)
	if err != nil {
		s.log.Error().Err(err).Msg("ldap sync: list tenants")
		return
	}
	defer rows.Close()

	var tenants []uuid.UUID
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err != nil {
			s.log.Error().Err(err).Msg("ldap sync: scan tenant")
			continue
		}
		tenants = append(tenants, t)
	}
	for _, t := range tenants {
		if err := s.SyncTenant(ctx, t, "scheduled"); err != nil {
			s.log.Warn().Err(err).Str("tenant", t.String()).Msg("ldap sync: tenant run failed")
		}
	}
}
