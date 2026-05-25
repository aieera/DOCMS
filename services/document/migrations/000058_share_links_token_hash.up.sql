-- Rename share_links.token -> share_links.token_hash to match the code.
--
-- The repo SQL in services/document/internal/repository/sharelink_repo.go
-- references `l.token_hash` in four places (INSERT + 3 SELECTs); the
-- original 000001 schema named the column `token`. Callers have always
-- stored the SHA-256 of the raw token here, never the plaintext (see the
-- comment at sharelink_repo.go:34) — the column name `token_hash` is
-- the accurate one. Without this rename, every authenticated call to
-- GET /api/v1/admin/share-links 500s with "column l.token_hash does not exist".
--
-- The UNIQUE constraint and existing index follow the column under the
-- rename automatically; no separate index drop/recreate needed.

ALTER TABLE share_links RENAME COLUMN token TO token_hash;
