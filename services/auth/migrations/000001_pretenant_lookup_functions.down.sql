DROP FUNCTION IF EXISTS auth_lookup_users_by_email(text, text);
DROP FUNCTION IF EXISTS auth_lookup_session_by_token(text);
DROP FUNCTION IF EXISTS auth_lookup_api_key_by_hash(text);
-- dms_auth_lookup is left in place: dropping a role requires reassigning
-- owned objects and it is harmless idle (NOLOGIN). Ops can DROP ROLE
-- manually after confirming no function depends on it.
