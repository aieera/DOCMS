-- QA SD-05: two folders could share a name under the same parent, making
-- the grid, the tree, and the breadcrumb path ambiguous — a real problem
-- for a file plan whose retention / legal-hold / e-discovery claims lean
-- on a stable path. Enforce uniqueness per (workspace, parent), live rows
-- only, case-insensitive (two names differing only in case are
-- indistinguishable in most listings and collide in exports).
--
-- Existing duplicates are renamed rather than failing the migration: all
-- but the oldest in each duplicate group get a collision-proof suffix
-- derived from their id (an ordinal like "(2)" could itself collide with
-- a pre-existing sibling). ltree paths are unaffected — labels are
-- id-suffixed at creation and rename never rewrites them.
UPDATE folders f
   SET name = f.name || ' [dup-' || substr(f.id::text, 1, 8) || ']',
       updated_at = now()
  FROM (
    SELECT tenant_id, id,
           row_number() OVER (
             PARTITION BY tenant_id, workspace_id,
                          COALESCE(parent_folder_id, '00000000-0000-0000-0000-000000000000'::uuid),
                          lower(name)
             ORDER BY created_at, id
           ) AS rn
      FROM folders
     WHERE deleted_at IS NULL
  ) d
 WHERE d.rn > 1 AND f.tenant_id = d.tenant_id AND f.id = d.id;

CREATE UNIQUE INDEX idx_folders_unique_name_per_parent
    ON folders (tenant_id, workspace_id,
                COALESCE(parent_folder_id, '00000000-0000-0000-0000-000000000000'::uuid),
                lower(name))
 WHERE deleted_at IS NULL;
