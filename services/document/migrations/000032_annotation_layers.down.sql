-- Undo ADR 0067 — narrow CHECKs back to pre-shipment values.
-- WARNING: rows whose values fall outside the narrower set will
-- prevent this migration from completing. Operators should
-- soft-delete or convert offending rows before rolling back.

ALTER TABLE annotations DROP CONSTRAINT IF EXISTS annotations_annotation_type_check;
ALTER TABLE annotations
    ADD CONSTRAINT annotations_annotation_type_check
        CHECK (annotation_type IN ('highlight', 'note', 'stamp', 'drawing'));

ALTER TABLE permissions DROP CONSTRAINT IF EXISTS permissions_capability_check;
ALTER TABLE permissions
    ADD CONSTRAINT permissions_capability_check
        CHECK (capability IN ('view', 'edit', 'delete', 'share', 'admin'));
