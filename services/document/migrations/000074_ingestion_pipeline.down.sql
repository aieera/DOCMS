-- Reverse the WS3 pre-commit ingestion pipeline. Drop children before
-- ingestion_items (FKs cascade, but be explicit).
DROP TABLE IF EXISTS review_queue_items;
DROP TABLE IF EXISTS ingestion_ocr;
DROP TABLE IF EXISTS ingestion_items;
