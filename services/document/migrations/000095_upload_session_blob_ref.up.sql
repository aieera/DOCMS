-- CompleteUpload idempotency (storage service).
--
-- A retried CompleteUpload used to re-run the whole completion pipeline:
-- the size check then compared the client's declared PLAINTEXT size
-- against the object that envelope encryption had already overwritten
-- with CIPHERTEXT, flipped the COMPLETED session to failed, and the
-- sha-mismatch branch could even delete the stored object. Retries after
-- a client timeout are normal, so this fired in the wild.
--
-- The fix replays the prior success instead: a completed session returns
-- its original result without re-validating or mutating anything. That
-- replay needs a durable pointer from the session to the blob it
-- produced (dedup hits reuse an EXISTING blob whose storage key differs
-- from the session's, so the key can't be derived).
ALTER TABLE upload_sessions
    ADD COLUMN IF NOT EXISTS content_blob_id UUID;
