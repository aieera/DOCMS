-- Wave A.1 (issue #75) — sanctioned pre-tenant lookup path.
--
-- Three auth reads happen BEFORE the tenant is known (the row IS how the
-- tenant is learned): API-key validation by key_hash, session fallback by
-- token_hash, and m365/Outlook email→user resolution. api_keys, sessions,
-- and users are all FORCE ROW LEVEL SECURITY, so the auth service's
-- dms_app (NOBYPASSRLS) role reads zero rows for these — API-key auth
-- 404s, a Redis cache-miss logs the user out, and m365 exchange dies.
--
-- CHOICE (documented per task): a SECURITY DEFINER exact-match lookup
-- function per read, owned by a minimal definer role, EXECUTE granted
-- only to dms_app. This is O(1) — the alternatives don't fit:
--   * tenant-enumeration (connector's LookupTokenTenant, #71) is
--     O(tenants) PER CALL; fine for infrequent SSE connects, unacceptable
--     for API-key auth (every request) / session fallback (every Redis
--     miss). #71 explicitly deferred the "proper" design to this issue.
--   * a scoped RLS policy cannot express "only the exact-match predicate"
--     — a USING clause can't see the query's WHERE — so it would be a
--     general table read bypass for dms_app, which the task forbids.
--
-- The definer role dms_auth_lookup has BYPASSRLS but NOLOGIN: nothing
-- connects as it; it exists only so these three fixed, parameterized,
-- exact-match functions can see across tenants. dms_app stays
-- NOBYPASSRLS and can do nothing cross-tenant except call these
-- functions. No general bypass; no row_security=off.
--
-- The tables live in the document schema-of-record (000001); these
-- functions are auth-owned logic over them, so they live in auth's chain.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dms_auth_lookup') THEN
    CREATE ROLE dms_auth_lookup NOLOGIN BYPASSRLS;
  ELSE
    ALTER ROLE dms_auth_lookup NOLOGIN BYPASSRLS;
  END IF;
END $$;

-- The definer role bypasses RLS but still needs the SQL SELECT privilege
-- on the tables the functions read (BYPASSRLS skips policies, not grants).
-- SELECT-only: these functions never write.
GRANT SELECT ON api_keys, sessions, users, organizations TO dms_auth_lookup;

-- API-key lookup by globally-unique key_hash (auth ValidateAPIKey).
CREATE OR REPLACE FUNCTION auth_lookup_api_key_by_hash(p_hash text)
RETURNS TABLE (
  tenant_id uuid, id uuid, user_id uuid, name text, key_hash text,
  key_prefix text, scopes text[], last_used_at timestamptz,
  expires_at timestamptz, created_at timestamptz, revoked_at timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT k.tenant_id, k.id,
         COALESCE(k.user_id, '00000000-0000-0000-0000-000000000000'::uuid),
         k.name, k.key_hash, k.key_prefix, k.scopes,
         k.last_used_at, k.expires_at, k.created_at, k.revoked_at
  FROM public.api_keys k
  WHERE k.key_hash = p_hash AND k.revoked_at IS NULL
$$;

-- Session lookup by globally-unique token_hash (auth ValidateSession
-- Redis-miss fallback).
CREATE OR REPLACE FUNCTION auth_lookup_session_by_token(p_hash text)
RETURNS TABLE (
  id uuid, tenant_id uuid, user_id uuid, token_hash text,
  ip_address text, user_agent text, expires_at timestamptz,
  last_activity_at timestamptz, created_at timestamptz, revoked_at timestamptz
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT s.id, s.tenant_id, s.user_id, s.token_hash,
         COALESCE(host(s.ip_address), ''), COALESCE(s.user_agent, ''),
         s.expires_at, s.last_activity_at, s.created_at, s.revoked_at
  FROM public.sessions s
  WHERE s.token_hash = p_hash AND s.revoked_at IS NULL
$$;

-- Email→user/tenant resolution for m365/Outlook exchange. Cross-tenant by
-- design (an email may exist in multiple tenants → 409 disambiguation);
-- p_tenant narrows when the caller already knows the tenant.
CREATE OR REPLACE FUNCTION auth_lookup_users_by_email(p_email text, p_tenant text)
RETURNS TABLE (tenant_id text, tenant_slug text, tenant_name text, user_id text)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
  SELECT u.tenant_id::text, o.slug, o.name, u.id::text
  FROM public.users u
  JOIN public.organizations o ON o.id = u.tenant_id
  WHERE lower(u.email) = lower(p_email)
    AND u.deleted_at IS NULL
    AND o.deleted_at IS NULL
    AND (p_tenant IS NULL OR u.tenant_id = p_tenant::uuid)
  ORDER BY o.created_at
$$;

-- Own the functions by the minimal definer role and expose EXECUTE only
-- to the app role (REVOKE from PUBLIC — no one else may call them).
ALTER FUNCTION auth_lookup_api_key_by_hash(text)   OWNER TO dms_auth_lookup;
ALTER FUNCTION auth_lookup_session_by_token(text)   OWNER TO dms_auth_lookup;
ALTER FUNCTION auth_lookup_users_by_email(text, text) OWNER TO dms_auth_lookup;

REVOKE ALL ON FUNCTION auth_lookup_api_key_by_hash(text)   FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_lookup_session_by_token(text)   FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_lookup_users_by_email(text, text) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION auth_lookup_api_key_by_hash(text)   TO dms_app;
GRANT EXECUTE ON FUNCTION auth_lookup_session_by_token(text)   TO dms_app;
GRANT EXECUTE ON FUNCTION auth_lookup_users_by_email(text, text) TO dms_app;
