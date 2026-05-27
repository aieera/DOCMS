-- Capture the request-context actor + client IP on every outbox row so
-- the audit consumer can populate audit_events.actor_name / ip_address
-- without each emitter having to re-pass these fields in its payload.
--
-- Before this migration, 0 / 192 audit_events rows had actor_name or
-- ip_address populated because the outbox -> NATS -> audit pipeline
-- only carried whatever the emitter chose to put into its inner JSON
-- payload, and no emitter included these. Plumbing them through the
-- outbox row itself means every event retroactively gains audit
-- context — no per-emitter edit needed.
--
-- All three fields are nullable. Background-worker emitters with no
-- HTTP context (OCR pipeline, scheduled sweeps, etc.) write NULL,
-- which the audit consumer will surface as empty actor — correct,
-- because there was no human actor.

ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS actor_id   UUID,
    ADD COLUMN IF NOT EXISTS actor_name TEXT,
    ADD COLUMN IF NOT EXISTS ip_address INET;
