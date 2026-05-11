-- 000003 — relax intel_processed_events.consumer CHECK constraint.
--
-- The original 000002 migration declared:
--     consumer TEXT NOT NULL CHECK (consumer IN ('classify', 'ner', 'embed'))
--
-- Since then ten more consumers landed in app/tasks/ — auto_tag,
-- smart_route, compliance_scan, lang_detect, translate, ocr_quality,
-- anomaly_detect, training_collector, model_retrain, model_evaluate.
-- Each of them hits intel_processed_events to dedupe NATS deliveries,
-- and every write blew up on the CHECK violation. The translate task
-- happened to be the first one a user clicked.
--
-- This migration drops the old constraint and re-adds it with the
-- full allow-list from app/intel_dedupe.py's Consumer Literal.

ALTER TABLE intel_processed_events
    DROP CONSTRAINT intel_processed_events_consumer_check;

ALTER TABLE intel_processed_events
    ADD CONSTRAINT intel_processed_events_consumer_check
    CHECK (consumer IN (
        'classify',
        'ner',
        'embed',
        'auto_tag',
        'smart_route',
        'compliance_scan',
        'lang_detect',
        'translate',
        'ocr_quality',
        'anomaly_detect',
        'training_collector',
        'model_retrain',
        'model_evaluate'
    ));
