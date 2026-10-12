-- Restore the 000001 definition (no owner-status filter).
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
