-- Review O-7 / L-10 — FORCE row level security on the intelligence
-- tables that only ENABLEd it (000001, 000002). ENABLE without FORCE
-- leaves the table OWNER unfiltered, and Helm and Ansible ran the
-- services as the owner. Every accessor (app/dedupe.py,
-- app/intel_dedupe.py, app/persist.py, the document service's routing
-- and OCR handlers) already sets app.current_tenant in its transaction.
--
-- document_entities is document-owned (ADR 0121); document 000108 forces it.
ALTER TABLE document_classifications FORCE ROW LEVEL SECURITY;
ALTER TABLE intel_processed_events   FORCE ROW LEVEL SECURITY;
ALTER TABLE ocr_processed_events     FORCE ROW LEVEL SECURITY;
