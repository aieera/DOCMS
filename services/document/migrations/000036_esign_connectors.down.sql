-- Reverse of 000036.

DROP INDEX IF EXISTS idx_esign_events_envelope;
DROP INDEX IF EXISTS idx_esign_events_dedup;
DROP TABLE IF EXISTS esign_envelope_events;
DROP TABLE IF EXISTS esign_oauth_tokens;
