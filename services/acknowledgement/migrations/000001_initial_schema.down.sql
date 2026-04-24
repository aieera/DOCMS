BEGIN;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS acknowledgement_signing_keys;
DROP TABLE IF EXISTS acknowledgement_events;
DROP TABLE IF EXISTS acknowledgement_assignments;
DROP TABLE IF EXISTS acknowledgement_campaigns;
COMMIT;
