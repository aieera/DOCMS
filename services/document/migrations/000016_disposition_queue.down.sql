-- Reverse of 000016_disposition_queue.
--
-- DOWN migrations in this codebase are best-effort for dev rollback.
-- A production rollback would also need to reconcile any candidate
-- rows whose dispositions had already been executed — those crypto-
-- shreds cannot be undone. Operators should NEVER run this in prod
-- without an audit-trail snapshot first.

DROP INDEX IF EXISTS idx_documents_shredded;
ALTER TABLE documents DROP COLUMN IF EXISTS shredded_at;

DROP TRIGGER IF EXISTS update_disposition_candidates_updated_at ON disposition_candidates;
DROP INDEX  IF EXISTS idx_disposition_candidates_review_queue;
DROP INDEX  IF EXISTS idx_disposition_candidates_active;
DROP TABLE  IF EXISTS disposition_candidates;
