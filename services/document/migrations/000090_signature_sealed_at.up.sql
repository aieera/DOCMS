-- Idempotency guard for the signing-ceremony seal (§sign). The seal consumer
-- runs off dms.signature.completed.v1 (at-least-once): on redelivery it would
-- re-fetch, re-sign N+1 times, and ingest a DUPLICATE sealed version. sealed_at
-- is an atomic claim marker — the consumer claims (SET sealed_at WHERE NULL)
-- before sealing and releases it on failure, so exactly one delivery seals.
ALTER TABLE signature_requests
    ADD COLUMN IF NOT EXISTS sealed_at TIMESTAMPTZ;
