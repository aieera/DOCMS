DROP TABLE IF EXISTS saved_search_subscribers;
ALTER TABLE saved_searches
    DROP COLUMN IF EXISTS workflow_id,
    DROP COLUMN IF EXISTS alert_frequency_cron,
    DROP COLUMN IF EXISTS last_match_doc_ids;
