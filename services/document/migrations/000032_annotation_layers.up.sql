-- ADR 0067 — annotation layers.
--
-- Two CHECK widenings:
--   1. annotations.annotation_type — add the three §10.5 categories
--      (pdf_markup, image_shape, video_timestamp) alongside the
--      existing primitives so old rows keep validating.
--   2. permissions.capability — add `annotation.create` and
--      `annotation.delete` so admins can grant markup-only access
--      separately from the heavier `edit` capability.

-- ---- 1. widen annotation_type ------------------------------------------
ALTER TABLE annotations
    DROP CONSTRAINT IF EXISTS annotations_annotation_type_check;

ALTER TABLE annotations
    ADD CONSTRAINT annotations_annotation_type_check
        CHECK (annotation_type IN (
            -- legacy primitives kept for back-compat with existing rows.
            'highlight', 'note', 'stamp', 'drawing',
            -- ADR 0067 categories.
            'pdf_markup', 'image_shape', 'video_timestamp'
        ));

-- ---- 2. widen permissions.capability -----------------------------------
ALTER TABLE permissions
    DROP CONSTRAINT IF EXISTS permissions_capability_check;

ALTER TABLE permissions
    ADD CONSTRAINT permissions_capability_check
        CHECK (capability IN (
            'view', 'edit', 'delete', 'share', 'admin',
            -- ADR 0067 fine-grained annotation capabilities.
            -- Note: `edit` still grants both via the resolution
            -- order in service.requireAnnotationPermission.
            'annotation.create', 'annotation.delete'
        ));
