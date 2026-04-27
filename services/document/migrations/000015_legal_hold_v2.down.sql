ALTER TABLE documents DROP COLUMN IF EXISTS hold_count;
DROP TABLE IF EXISTS legal_hold_events;
DROP TABLE IF EXISTS legal_hold_targets;
DROP TABLE IF EXISTS legal_hold_custodians;
