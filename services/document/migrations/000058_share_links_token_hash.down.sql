-- Reverse of 000058 — rename token_hash back to token.
ALTER TABLE share_links RENAME COLUMN token_hash TO token;
