-- Native review/triage queue (Workstream 4). Converges WS3's review_queue_items
-- table onto the WS4 surface: a first-class `review_queue` with pending/resolved/
-- rejected status, served by the top-level /api/v1/review-queue endpoints. The
-- table already exists (migration 000074) carrying the routing-time fields; this
-- migration only renames it + aligns the status vocabulary and the match column,
-- so no data is lost. Append-only (not an edit of 000074) per the repo's
-- migration discipline — safe whether or not 000074 was already applied.

ALTER TABLE review_queue_items RENAME TO review_queue;
ALTER TABLE review_queue RENAME COLUMN candidate_document_id TO suggested_match_document_id;

-- status: 'open' → 'pending' (WS4 vocabulary).
ALTER TABLE review_queue ALTER COLUMN status SET DEFAULT 'pending';
UPDATE review_queue SET status = 'pending' WHERE status = 'open';
ALTER TABLE review_queue DROP CONSTRAINT review_queue_items_status_check;
ALTER TABLE review_queue ADD CONSTRAINT review_queue_status_check
    CHECK (status IN ('pending','resolved','rejected'));

-- resolution: speak the WS4 decision vocabulary (new_version | new_document |
-- rejected) so the stored audit value matches the resolve API + event.
UPDATE review_queue SET resolution = 'new_version'  WHERE resolution = 'committed_version';
UPDATE review_queue SET resolution = 'new_document' WHERE resolution = 'committed_new';
ALTER TABLE review_queue DROP CONSTRAINT review_queue_items_resolution_check;
ALTER TABLE review_queue ADD CONSTRAINT review_queue_resolution_check
    CHECK (resolution IS NULL OR resolution IN ('new_version','new_document','rejected'));
