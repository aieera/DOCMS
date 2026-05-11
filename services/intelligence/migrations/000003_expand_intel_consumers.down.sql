-- Rollback: re-apply the original 3-consumer constraint.
-- Note: any rows written under the post-000003 consumers will fail this
-- check and the migration will refuse to run. Delete those rows first if
-- you actually need to roll back (you probably don't).

ALTER TABLE intel_processed_events
    DROP CONSTRAINT intel_processed_events_consumer_check;

ALTER TABLE intel_processed_events
    ADD CONSTRAINT intel_processed_events_consumer_check
    CHECK (consumer IN ('classify', 'ner', 'embed'));
