-- H-9 / DEC-9: persist each upload session's object location.
--
-- The storage service derived an upload's object key from the CURRENT
-- UTC year/month every time it needed it ({tenant}/{yyyy}/{mm}/{id}/{name}).
-- Initiate, complete, abort and download each recomputed it, so a
-- complete that crossed a month boundary looked for the object in the
-- wrong prefix ("object missing in storage"), abort deleted a key that
-- never existed, and every presigned download of an unencrypted blob
-- uploaded in an earlier month pointed at a non-existent key (404).
--
-- Initiate now writes the bucket and key it signed the PUT URL for, and
-- complete/abort/download read them back. Both columns stay nullable:
-- sessions created before this migration keep NULL, and the storage
-- service falls back to the key recomputed from created_at (the month the
-- legacy initiate actually used), so pre-existing sessions still
-- complete, abort and download.
ALTER TABLE upload_sessions
    ADD COLUMN IF NOT EXISTS storage_bucket TEXT,
    ADD COLUMN IF NOT EXISTS storage_key    TEXT;
