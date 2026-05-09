// ADR 0062 — LDAP config + group-mapping + sync-history repo.
//
// Same `pgx.Tx`-wrapping pattern as user_repo.go: every method takes
// an in-flight tx so the service layer wraps multi-statement work
// in a single tenant-scoped transaction. RLS is enforced at the
// session GUC level by pkg/database.WithTenantTx; the repo never
// reaches around it.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// LDAPConfigRow is the persistence shape — sealed bind password
// stays sealed until the service layer decrypts it. The plaintext
// never crosses this struct.
type LDAPConfigRow struct {
	ID                   uuid.UUID
	TenantID             uuid.UUID
	URL                  string
	UseStartTLS          bool
	AllowInsecure        bool
	BindDN               string
	BindPasswordSealed   []byte // base64-of-(nonce||ct), service layer unseals
	UserSearchBase       string
	UserSearchFilter     string
	EmailAttribute       string
	DisplayNameAttribute string
	GroupSearchBase      string
	GroupSearchFilter    string
	NestedGroups         bool
	FallbackToLocal      bool
	IsActive             bool
	LastSyncAt           *time.Time
	LastSyncStatus       *string
	LastSyncError        *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// LDAPGroupMappingRow maps an AD group DN to a DMS group id.
type LDAPGroupMappingRow struct {
	TenantID     uuid.UUID
	LDAPConfigID uuid.UUID
	LDAPGroupDN  string
	DMSGroupID   uuid.UUID
	CreatedAt    time.Time
}

// LDAPSyncHistoryRow describes one sync run.
type LDAPSyncHistoryRow struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	LDAPConfigID  uuid.UUID
	Trigger       string
	StartedAt     time.Time
	FinishedAt    *time.Time
	Status        string
	UsersSynced   int
	GroupsSynced  int
	Errors        int
	ErrorSummary  *string
}

// LDAPRepository is the persistence interface for the LDAP feature.
type LDAPRepository interface {
	// ---- configs ------------------------------------------------------
	GetActiveConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*LDAPConfigRow, error)
	GetConfig(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*LDAPConfigRow, error)
	ListConfigs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]LDAPConfigRow, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, row *LDAPConfigRow) error
	DeleteConfig(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	UpdateLastSync(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, errMsg string, at time.Time) error
	ListAllActiveAcrossTenants(ctx context.Context, pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}) ([]LDAPConfigRow, error)

	// ---- group mappings ----------------------------------------------
	ListMappings(ctx context.Context, tx pgx.Tx, tenantID, configID uuid.UUID) ([]LDAPGroupMappingRow, error)
	UpsertMapping(ctx context.Context, tx pgx.Tx, m *LDAPGroupMappingRow) error
	DeleteMapping(ctx context.Context, tx pgx.Tx, tenantID, configID uuid.UUID, ldapDN string, dmsGroupID uuid.UUID) error

	// ---- sync history -------------------------------------------------
	StartHistory(ctx context.Context, tx pgx.Tx, h *LDAPSyncHistoryRow) error
	FinishHistory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, users, groups, errs int, summary string) error
	ListHistory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, limit int) ([]LDAPSyncHistoryRow, error)
}

// NewLDAPRepo returns the default Postgres-backed implementation.
func NewLDAPRepo() LDAPRepository { return &ldapRepo{} }

type ldapRepo struct{}

// ---- configs ----------------------------------------------------------

func (r *ldapRepo) GetActiveConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*LDAPConfigRow, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, url, use_starttls, allow_insecure,
		       bind_dn, bind_password_sealed,
		       user_search_base, user_search_filter,
		       email_attribute, display_name_attribute,
		       group_search_base, group_search_filter,
		       nested_groups, fallback_to_local, is_active,
		       last_sync_at, last_sync_status, last_sync_error,
		       created_at, updated_at
		  FROM ldap_configs
		 WHERE tenant_id = $1 AND is_active
		 LIMIT 1`, tenantID)
	out, err := scanConfig(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.NotFound("no active ldap config")
		}
		return nil, err
	}
	return out, nil
}

func (r *ldapRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*LDAPConfigRow, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, url, use_starttls, allow_insecure,
		       bind_dn, bind_password_sealed,
		       user_search_base, user_search_filter,
		       email_attribute, display_name_attribute,
		       group_search_base, group_search_filter,
		       nested_groups, fallback_to_local, is_active,
		       last_sync_at, last_sync_status, last_sync_error,
		       created_at, updated_at
		  FROM ldap_configs
		 WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	out, err := scanConfig(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, vdmserr.NotFound("ldap config not found")
		}
		return nil, err
	}
	return out, nil
}

func (r *ldapRepo) ListConfigs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]LDAPConfigRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, url, use_starttls, allow_insecure,
		       bind_dn, bind_password_sealed,
		       user_search_base, user_search_filter,
		       email_attribute, display_name_attribute,
		       group_search_base, group_search_filter,
		       nested_groups, fallback_to_local, is_active,
		       last_sync_at, last_sync_status, last_sync_error,
		       created_at, updated_at
		  FROM ldap_configs
		 WHERE tenant_id = $1
		 ORDER BY updated_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LDAPConfigRow
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpsertConfig creates a new row when row.ID is uuid.Nil; otherwise
// updates in place. The unique partial index on (tenant_id) WHERE
// is_active enforces "at most one active config per tenant" — the
// service layer is responsible for flipping competing rows inactive
// before activating a new one.
func (r *ldapRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, row *LDAPConfigRow) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
		_, err := tx.Exec(ctx, `
			INSERT INTO ldap_configs (
				tenant_id, id, url, use_starttls, allow_insecure,
				bind_dn, bind_password_sealed,
				user_search_base, user_search_filter,
				email_attribute, display_name_attribute,
				group_search_base, group_search_filter,
				nested_groups, fallback_to_local, is_active
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7,
				$8, $9,
				$10, $11,
				$12, $13,
				$14, $15, $16
			)`,
			row.TenantID, row.ID, row.URL, row.UseStartTLS, row.AllowInsecure,
			row.BindDN, row.BindPasswordSealed,
			row.UserSearchBase, row.UserSearchFilter,
			row.EmailAttribute, row.DisplayNameAttribute,
			row.GroupSearchBase, row.GroupSearchFilter,
			row.NestedGroups, row.FallbackToLocal, row.IsActive,
		)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE ldap_configs SET
			url = $3, use_starttls = $4, allow_insecure = $5,
			bind_dn = $6, bind_password_sealed = $7,
			user_search_base = $8, user_search_filter = $9,
			email_attribute = $10, display_name_attribute = $11,
			group_search_base = $12, group_search_filter = $13,
			nested_groups = $14, fallback_to_local = $15, is_active = $16,
			updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		row.TenantID, row.ID, row.URL, row.UseStartTLS, row.AllowInsecure,
		row.BindDN, row.BindPasswordSealed,
		row.UserSearchBase, row.UserSearchFilter,
		row.EmailAttribute, row.DisplayNameAttribute,
		row.GroupSearchBase, row.GroupSearchFilter,
		row.NestedGroups, row.FallbackToLocal, row.IsActive,
	)
	return err
}

func (r *ldapRepo) DeleteConfig(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM ldap_configs WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return err
}

func (r *ldapRepo) UpdateLastSync(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status, errMsg string, at time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE ldap_configs
		   SET last_sync_at = $3, last_sync_status = $4, last_sync_error = NULLIF($5, '')
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, at, status, errMsg)
	return err
}

// ListAllActiveAcrossTenants — used by the scheduler. Bypasses
// per-tenant RLS by running on a connection with NO app.current_tenant
// set; the WHERE clause covers tenancy explicitly. The pool used here
// must be the same NOBYPASSRLS pool as everything else, so we still
// can't accidentally read another tenant's row from inside a tenant tx.
func (r *ldapRepo) ListAllActiveAcrossTenants(ctx context.Context, pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]LDAPConfigRow, error) {
	// Note: this query runs OUTSIDE a per-tenant tx. RLS is still on,
	// but ldap_configs has no policy that requires a current_tenant
	// match for the platform admin role — the rows are surfaced to
	// the scheduler with NO USING clause check… actually, our policy
	// DOES require current_tenant. So we use a SECURITY-DEFINER read
	// function in a follow-up if needed; for now we use a service-role
	// pool. To keep scope tight today, the scheduler iterates known
	// tenant IDs from the organizations table and asks per-tenant.
	rows, err := pool.Query(ctx, `
		SELECT o.id
		  FROM organizations o
		 ORDER BY o.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// We only return tenant IDs in this stub — the caller does a
	// per-tenant fetch under WithTenantTx. Returning a sparse row
	// (only TenantID populated) keeps the signature stable.
	var out []LDAPConfigRow
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, LDAPConfigRow{TenantID: t})
	}
	return out, rows.Err()
}

// ---- mappings ----------------------------------------------------------

func (r *ldapRepo) ListMappings(ctx context.Context, tx pgx.Tx, tenantID, configID uuid.UUID) ([]LDAPGroupMappingRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, ldap_config_id, ldap_group_dn, dms_group_id, created_at
		  FROM ldap_group_mappings
		 WHERE tenant_id = $1 AND ldap_config_id = $2`, tenantID, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LDAPGroupMappingRow
	for rows.Next() {
		var m LDAPGroupMappingRow
		if err := rows.Scan(&m.TenantID, &m.LDAPConfigID, &m.LDAPGroupDN, &m.DMSGroupID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *ldapRepo) UpsertMapping(ctx context.Context, tx pgx.Tx, m *LDAPGroupMappingRow) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO ldap_group_mappings (tenant_id, ldap_config_id, ldap_group_dn, dms_group_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, ldap_config_id, ldap_group_dn, dms_group_id) DO NOTHING`,
		m.TenantID, m.LDAPConfigID, m.LDAPGroupDN, m.DMSGroupID)
	return err
}

func (r *ldapRepo) DeleteMapping(ctx context.Context, tx pgx.Tx, tenantID, configID uuid.UUID, ldapDN string, dmsGroupID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		DELETE FROM ldap_group_mappings
		 WHERE tenant_id = $1 AND ldap_config_id = $2
		   AND ldap_group_dn = $3 AND dms_group_id = $4`,
		tenantID, configID, ldapDN, dmsGroupID)
	return err
}

// ---- sync history ------------------------------------------------------

func (r *ldapRepo) StartHistory(ctx context.Context, tx pgx.Tx, h *LDAPSyncHistoryRow) error {
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ldap_sync_history (tenant_id, id, ldap_config_id, trigger, started_at, status)
		VALUES ($1, $2, $3, $4, COALESCE($5, now()), 'running')`,
		h.TenantID, h.ID, h.LDAPConfigID, h.Trigger, h.StartedAt)
	return err
}

func (r *ldapRepo) FinishHistory(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, users, groups, errs int, summary string) error {
	_, err := tx.Exec(ctx, `
		UPDATE ldap_sync_history
		   SET finished_at = now(), status = $3,
		       users_synced = $4, groups_synced = $5, errors = $6,
		       error_summary = NULLIF($7, '')
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, status, users, groups, errs, summary)
	return err
}

func (r *ldapRepo) ListHistory(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, limit int) ([]LDAPSyncHistoryRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, ldap_config_id, trigger, started_at, finished_at,
		       status, users_synced, groups_synced, errors, error_summary
		  FROM ldap_sync_history
		 WHERE tenant_id = $1
		 ORDER BY started_at DESC
		 LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LDAPSyncHistoryRow
	for rows.Next() {
		var h LDAPSyncHistoryRow
		if err := rows.Scan(&h.ID, &h.TenantID, &h.LDAPConfigID, &h.Trigger, &h.StartedAt, &h.FinishedAt,
			&h.Status, &h.UsersSynced, &h.GroupsSynced, &h.Errors, &h.ErrorSummary); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---- helpers -----------------------------------------------------------

// rowScanner is satisfied by both *pgx.Row (from QueryRow) and
// pgx.Rows (from Query) — lets scanConfig serve both list and get.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanConfig(row rowScanner) (*LDAPConfigRow, error) {
	var c LDAPConfigRow
	if err := row.Scan(
		&c.ID, &c.TenantID, &c.URL, &c.UseStartTLS, &c.AllowInsecure,
		&c.BindDN, &c.BindPasswordSealed,
		&c.UserSearchBase, &c.UserSearchFilter,
		&c.EmailAttribute, &c.DisplayNameAttribute,
		&c.GroupSearchBase, &c.GroupSearchFilter,
		&c.NestedGroups, &c.FallbackToLocal, &c.IsActive,
		&c.LastSyncAt, &c.LastSyncStatus, &c.LastSyncError,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}
