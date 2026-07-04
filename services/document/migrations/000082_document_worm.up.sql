-- Document WORM retention (blob object-lock, E... blob-WORM). worm_retain_until
-- mirrors the S3 object-lock retention applied to the document's blob, so the
-- document service can refuse overwrite/delete before the date (defense in
-- depth alongside the S3-enforced lock) and the UI can show a WORM indicator.
ALTER TABLE documents
    ADD COLUMN IF NOT EXISTS worm_retain_until TIMESTAMPTZ;
