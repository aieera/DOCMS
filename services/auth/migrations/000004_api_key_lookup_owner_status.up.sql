-- Review remediation (M-8, API keys) — a key whose owning user is
-- suspended, deactivated or soft-deleted must stop authenticating.
--
-- SuspendUser revokes a user's keys, but other status paths (SCIM
-- deactivate / PATCH active=false, admin edits, soft delete) do not, and
-- auth_lookup_api_key_by_hash (000001) never looked at the owner. Both the
-- auth service's ValidateAPIKey and the shared pkg/middleware APIKeyAuth
-- resolve keys through this function, so filtering here closes the gap
-- for every service at once.
--
-- Same signature, return shape, owner (dms_auth_lookup) and grants as
-- 000001 — CREATE OR REPLACE keeps the owner and the EXECUTE ACL. Keys
-- with no owner (user_id NULL, service keys) are unaffected. The definer
-- role already holds SELECT on users (000001).
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
  LEFT JOIN public.users u ON u.tenant_id = k.tenant_id AND u.id = k.user_id
  WHERE k.key_hash = p_hash
    AND k.revoked_at IS NULL
    AND (k.user_id IS NULL
         OR (u.status = 'active' AND u.deleted_at IS NULL))
$$;

ALTER FUNCTION auth_lookup_api_key_by_hash(text) OWNER TO dms_auth_lookup;
REVOKE ALL ON FUNCTION auth_lookup_api_key_by_hash(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_lookup_api_key_by_hash(text) TO dms_app;
