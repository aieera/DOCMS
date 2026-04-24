package repository

import (
	"context"
	"net/netip"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

// GeofenceRepo is the CRUD surface for `geofence_policies`. All reads
// and writes run inside a tenant-scoped tx (database.WithTenantTx) so
// RLS enforces tenant isolation; callers MUST NOT pass a raw pool
// connection.
type GeofenceRepo interface {
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.GeofencePolicy, error)
	ListForScope(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scopes []model.GeofenceScope, scopeIDs []uuid.UUID) ([]model.GeofencePolicy, error)
	Get(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.GeofencePolicy, error)
	Insert(ctx context.Context, tx pgx.Tx, p *model.GeofencePolicy) error
	Update(ctx context.Context, tx pgx.Tx, p *model.GeofencePolicy) error
	Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
}

type geofenceRepo struct{}

// NewGeofenceRepo returns a stateless repo.
func NewGeofenceRepo() GeofenceRepo { return &geofenceRepo{} }

const selectGeofenceSQL = `
	SELECT tenant_id, id, scope, scope_id, mode,
	       COALESCE(country_codes, '{}'::text[]),
	       COALESCE(cidr_allowlist::text[], '{}'::text[]),
	       COALESCE(cidr_denylist::text[],  '{}'::text[]),
	       apply_to, enabled, created_by_user_id, created_at, updated_at
	  FROM geofence_policies`

func (r *geofenceRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.GeofencePolicy, error) {
	rows, err := tx.Query(ctx, selectGeofenceSQL+` WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	return scanGeofenceRows(rows)
}

// ListForScope returns only policies that apply to the supplied
// (scope, scope_id) pairs — used by the enforcement middleware to
// avoid loading every policy in the tenant on every request.
func (r *geofenceRepo) ListForScope(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scopes []model.GeofenceScope, scopeIDs []uuid.UUID) ([]model.GeofencePolicy, error) {
	// Always include tenant-scope policies (no scope_id).
	rows, err := tx.Query(ctx, selectGeofenceSQL+`
		WHERE tenant_id = $1
		  AND enabled = true
		  AND (
		        (scope = 'tenant')
		     OR (scope = ANY($2) AND scope_id = ANY($3))
		      )
		ORDER BY scope ASC, created_at ASC`,
		tenantID, scopesToStrings(scopes), scopeIDs)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	return scanGeofenceRows(rows)
}

func (r *geofenceRepo) Get(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.GeofencePolicy, error) {
	row := tx.QueryRow(ctx, selectGeofenceSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	policies, err := scanGeofenceRows(&singleRow{row: row})
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, mapPgError(pgx.ErrNoRows)
	}
	return &policies[0], nil
}

func (r *geofenceRepo) Insert(ctx context.Context, tx pgx.Tx, p *model.GeofencePolicy) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO geofence_policies (
		    tenant_id, id, scope, scope_id, mode,
		    country_codes, cidr_allowlist, cidr_denylist,
		    apply_to, enabled, created_by_user_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7::cidr[], $8::cidr[], $9, $10, $11)`,
		p.TenantID, p.ID, string(p.Scope), p.ScopeID, string(p.Mode),
		nilIfEmpty(p.CountryCodes),
		prefixesToStrings(p.CIDRAllowlist),
		prefixesToStrings(p.CIDRDenylist),
		string(p.ApplyTo), p.Enabled, p.CreatedByUserID,
	)
	return mapPgError(err)
}

func (r *geofenceRepo) Update(ctx context.Context, tx pgx.Tx, p *model.GeofencePolicy) error {
	tag, err := tx.Exec(ctx, `
		UPDATE geofence_policies
		   SET mode = $3,
		       country_codes = $4,
		       cidr_allowlist = $5::cidr[],
		       cidr_denylist  = $6::cidr[],
		       apply_to = $7,
		       enabled  = $8,
		       updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		p.TenantID, p.ID, string(p.Mode),
		nilIfEmpty(p.CountryCodes),
		prefixesToStrings(p.CIDRAllowlist),
		prefixesToStrings(p.CIDRDenylist),
		string(p.ApplyTo), p.Enabled,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return mapPgError(pgx.ErrNoRows)
	}
	return nil
}

func (r *geofenceRepo) Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM geofence_policies WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return mapPgError(pgx.ErrNoRows)
	}
	return nil
}

// ---- scan helpers ---------------------------------------------------------

// rowScanner is pgx.Rows or a single-row adapter.
type rowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

type singleRow struct {
	row    pgx.Row
	done   bool
	err    error
}

func (s *singleRow) Next() bool {
	if s.done {
		return false
	}
	s.done = true
	return true
}
func (s *singleRow) Scan(dest ...any) error {
	if err := s.row.Scan(dest...); err != nil {
		s.err = err
		return err
	}
	return nil
}
func (s *singleRow) Err() error { return s.err }

func scanGeofenceRows(rows rowScanner) ([]model.GeofencePolicy, error) {
	var out []model.GeofencePolicy
	for rows.Next() {
		var (
			p       model.GeofencePolicy
			scope   string
			mode    string
			apply   string
			allow   []string
			deny    []string
			scopeID *uuid.UUID
			createdBy *uuid.UUID
		)
		if err := rows.Scan(
			&p.TenantID, &p.ID, &scope, &scopeID, &mode,
			&p.CountryCodes, &allow, &deny,
			&apply, &p.Enabled, &createdBy, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, mapPgError(err)
		}
		p.Scope = model.GeofenceScope(scope)
		p.Mode = model.GeofenceMode(mode)
		p.ApplyTo = model.GeofenceAction(apply)
		p.ScopeID = scopeID
		p.CreatedByUserID = createdBy
		p.CIDRAllowlist = stringsToPrefixes(allow)
		p.CIDRDenylist = stringsToPrefixes(deny)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return out, nil
}

// ---- slice helpers --------------------------------------------------------

func scopesToStrings(in []model.GeofenceScope) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func prefixesToStrings(in []netip.Prefix) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = p.String()
	}
	return out
}

// stringsToPrefixes parses back from the `cidr[]` string-cast in the
// SELECT. Invalid entries are silently dropped — the constraint on
// insert guarantees validity; this guard only catches corruption
// outside the service.
func stringsToPrefixes(in []string) []netip.Prefix {
	if len(in) == 0 {
		return nil
	}
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func nilIfEmpty(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s
}
