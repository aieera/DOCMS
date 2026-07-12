-- Distributed tracing: carry the producing request's W3C trace context
-- THROUGH the transactional outbox so a consumer's span links back to it.
--
-- NATS has no OpenTelemetry instrumentation, so the async hop (service
-- write → outbox → publisher → NATS → consumer) breaks a trace unless the
-- context rides the row. OutboxRepository.Insert stamps the current
-- trace context here at write time; OutboxPublisher injects it as the
-- `traceparent` header on the NATS message; the consumer re-extracts it.
--
-- Nullable: events produced without an active span (background sweeps)
-- store NULL and simply start a fresh root trace on the consumer side.
ALTER TABLE outbox
    ADD COLUMN IF NOT EXISTS trace_context JSONB;
