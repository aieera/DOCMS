-- Restore default (NOBYPASSRLS = unspecified). We don't actively
-- elevate to BYPASSRLS because that's the dangerous state — leaving
-- both roles at the schema's intended posture is the only correct
-- "down". This is essentially a no-op rollback for safety.
SELECT 'fix-9 down is intentionally a no-op; NOBYPASSRLS is the safe default' AS note;
