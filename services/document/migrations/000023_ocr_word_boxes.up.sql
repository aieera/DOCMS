-- ADR 0061 follow-up — per-word bounding boxes for entity overlay on
-- the PDF viewer. Stored as a JSONB array of word records:
--
--   [{"start": 1234, "end": 1238,
--     "x0": 72.0, "y0": 134.5, "x1": 102.7, "y1": 148.2}, ...]
--
-- "start" / "end" are character offsets within THIS page's
-- text_content (page-relative, not document-wide). The frontend
-- already rebases document_entities offsets to per-page via
-- entitiesForPage, so a single (start, end) coordinate space is
-- enough to look words up. PDF coordinates are pymupdf's top-left
-- origin with y-down, in PDF user-space points — frontend places
-- rectangles directly without further transforms.
--
-- Why a separate column instead of folding into bounding_boxes:
-- bounding_boxes is the Surya line-level box payload (existing
-- consumers depend on its shape). Word boxes are produced by a
-- different path (pymupdf.get_text("words")), have a different
-- granularity, and most importantly carry the char offsets that link
-- back to entity spans. Mixing them would force every reader to
-- branch on a "type" tag.

ALTER TABLE ocr_results
    ADD COLUMN word_boxes JSONB NOT NULL DEFAULT '[]'::jsonb;

-- No index on the JSONB itself — overlay lookups always go via
-- (tenant_id, version_id) which idx_ocr_version_page already covers,
-- and the JSONB read happens only on the doc-detail view (low QPS).
