DROP FUNCTION IF EXISTS outbox_drain_stats();
DROP FUNCTION IF EXISTS outbox_claim_batch(int);
REVOKE ALL ON outbox, outbox_dlq FROM dms_outbox_drain;
-- dms_outbox_drain is left in place (NOLOGIN, owns nothing now): roles
-- are cluster-wide and may be referenced from another database. Ops can
-- DROP ROLE manually. The dms_app grants are left as well — they match
-- what deployment default privileges already give the runtime role.
