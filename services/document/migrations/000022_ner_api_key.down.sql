ALTER TABLE ner_config
    DROP COLUMN IF EXISTS llm_api_key_set_at,
    DROP COLUMN IF EXISTS llm_api_key_encrypted;
