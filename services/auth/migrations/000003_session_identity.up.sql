-- C-3 / M-8 — sanctioned pre-tenant session lookup for the SHARED
-- middleware (pkg/middleware SessionAuth / SessionAuthOptional), used by
-- every non-auth service.
--
-- SessionAuth learns the tenant FROM the session row, so it cannot run
-- inside WithTenantTx; sessions and users are FORCE ROW LEVEL SECURITY,
-- so under the production dms_app (NOBYPASSRLS) role its raw-pool JOIN
-- read zero rows and every non-auth service answered 401. Same shape as
-- 000001: one fixed, exact-match SECURITY DEFINER function owned by the
-- NOLOGIN BYPASSRLS definer role dms_auth_lookup, search_path pinned,
-- EXECUTE revoked from PUBLIC and granted to dms_app only.
--
-- It returns the minimum the middleware needs (tenant, user, email, role,
-- status, expiry) and only for a live session (not revoked, not expired)
-- of an ACTIVE, non-deleted user (M-8: a suspended user's session used to
-- keep working on every other service until it expired). status is
-- returned for the caller's logs/assertions; it is always 'active'.
--
-- dms_auth_lookup already holds SELECT on sessions and users (000001).

CREATE OR REPLACE FUNCTION auth_session_identity(p_hash text)
RETURNS TABLE (
  tenant_id uuid, user_id uuid, email text, role text, status text,
  expires_at timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT s.tenant_id, s.user_id, u.email, u.role, u.status, s.expires_at
  FROM public.sessions s
  JOIN public.users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
  WHERE s.token_hash = p_hash
    AND s.revoked_at IS NULL
    AND s.expires_at > now()
    AND u.deleted_at IS NULL
    AND u.status = 'active'
$$;

ALTER FUNCTION auth_session_identity(text) OWNER TO dms_auth_lookup;
REVOKE ALL ON FUNCTION auth_session_identity(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_session_identity(text) TO dms_app;
