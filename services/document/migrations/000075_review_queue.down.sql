-- Reverse the WS4 convergence: restore the WS3 review_queue_items shape.
ALTER TABLE review_queue DROP CONSTRAINT review_queue_resolution_check;
UPDATE review_queue SET resolution = 'committed_version' WHERE resolution = 'new_version';
UPDATE review_queue SET resolution = 'committed_new'     WHERE resolution = 'new_document';
ALTER TABLE review_queue ADD CONSTRAINT review_queue_items_resolution_check
    CHECK (resolution IS NULL OR resolution IN ('committed_version','committed_new','rejected'));
ALTER TABLE review_queue DROP CONSTRAINT review_queue_status_check;
UPDATE review_queue SET status = 'open' WHERE status = 'pending';
ALTER TABLE review_queue ALTER COLUMN status SET DEFAULT 'open';
ALTER TABLE review_queue ADD CONSTRAINT review_queue_items_status_check
    CHECK (status IN ('open','resolved','rejected'));
ALTER TABLE review_queue RENAME COLUMN suggested_match_document_id TO candidate_document_id;
ALTER TABLE review_queue RENAME TO review_queue_items;
