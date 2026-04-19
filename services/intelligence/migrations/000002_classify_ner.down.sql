BEGIN;
DROP POLICY IF EXISTS intel_processed_events_tenant_isolation ON intel_processed_events;
DROP TABLE IF EXISTS intel_processed_events;

DROP POLICY IF EXISTS document_entities_tenant_isolation ON document_entities;
DROP TABLE IF EXISTS document_entities;

DROP POLICY IF EXISTS document_classifications_tenant_isolation ON document_classifications;
DROP TABLE IF EXISTS document_classifications;
COMMIT;
