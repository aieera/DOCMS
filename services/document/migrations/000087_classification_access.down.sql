DROP TABLE IF EXISTS classification_gate_config;
DROP TABLE IF EXISTS classification_access_rules;

ALTER TABLE users
    DROP COLUMN IF EXISTS clearance;

DROP INDEX IF EXISTS idx_documents_security_classification;

ALTER TABLE documents
    DROP COLUMN IF EXISTS security_classification,
    DROP COLUMN IF EXISTS has_phi,
    DROP COLUMN IF EXISTS has_pii,
    DROP COLUMN IF EXISTS classification_source;
