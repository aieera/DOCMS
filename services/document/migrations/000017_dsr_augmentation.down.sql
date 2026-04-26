-- Reverse of 000017_dsr_augmentation.
--
-- ⚠ Production rollback caveats:
--   - dsr_request_artifacts rows reference S3 objects that this
--     migration does NOT delete. Operators should reconcile orphaned
--     ZIPs separately (or accept they'll be reaped by storage-side
--     orphan sweeper).
--   - dsr_conflicts rows are append-only audit material; dropping the
--     table loses resolution history. Snapshot before running.

DROP TABLE IF EXISTS dsr_conflicts;
DROP TABLE IF EXISTS dsr_request_artifacts;

DROP INDEX IF EXISTS idx_dsr_status_token;

ALTER TABLE privacy_dsr_requests
    DROP COLUMN IF EXISTS last_sla_notified_at,
    DROP COLUMN IF EXISTS intake_source,
    DROP COLUMN IF EXISTS status_token_hash,
    DROP COLUMN IF EXISTS requester_identity_verified_at;
