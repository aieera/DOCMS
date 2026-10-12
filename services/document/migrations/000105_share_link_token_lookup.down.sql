DROP FUNCTION IF EXISTS share_link_lookup_by_token_hash(text);
REVOKE SELECT ON share_links FROM dms_token_lookup;
-- dms_token_lookup is left in place: 000109/000113 functions may still be
-- owned by it, and it is harmless idle (NOLOGIN). Ops can DROP ROLE once no
-- function depends on it.
