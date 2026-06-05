-- content_blobs.shredded_at — crypto-shredding bookkeeping.
--
-- Three handlers already query `shredded_at IS NULL` (decrypt_stream,
-- sharing_tags version download, compliance_overview blob counts) but the
-- column was never created — so those queries failed at runtime ("column
-- shredded_at does not exist"), which 404'd decrypt-stream and broke the
-- signature server-seal fetch. Add the column (nullable; NULL = live blob).
-- The primary shred mechanism remains KEK-drop (crypto-shredding); this column
-- records an explicit shred timestamp when one is performed.
ALTER TABLE content_blobs ADD COLUMN IF NOT EXISTS shredded_at TIMESTAMPTZ;
