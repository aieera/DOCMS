-- Reverse of 000034. Drops the three new tables; leaves the
-- digest_enabled column in place because dropping a column is
-- destructive (silently loses any per-cell settings users have
-- saved). Re-run 000034 to re-add the rest if needed.

DROP INDEX IF EXISTS idx_notification_digests_sweep;
DROP INDEX IF EXISTS idx_notification_digests_open;
DROP TABLE IF EXISTS notification_digests;

DROP TABLE IF EXISTS notification_dnd;

DROP INDEX IF EXISTS idx_notification_snoozes_active;
DROP TABLE IF EXISTS notification_snoozes;

ALTER TABLE notification_preferences DROP COLUMN IF EXISTS digest_enabled;
