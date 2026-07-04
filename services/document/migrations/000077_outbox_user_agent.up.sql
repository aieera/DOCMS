-- Carry the request-context User-Agent on every outbox row, mirroring the
-- actor_id / actor_name / ip_address columns added in 000059. The outbox
-- publisher copies it into the CloudEvents envelope (vdmsuseragent) so the
-- audit consumer can populate audit_events.user_agent without each emitter
-- having to thread it through its payload.
--
-- Nullable: background-worker emitters with no HTTP context (OCR pipeline,
-- scheduled sweeps, NATS consumers) write NULL, surfaced as empty
-- user_agent — correct, because there was no browser/device actor.

ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS user_agent TEXT;
