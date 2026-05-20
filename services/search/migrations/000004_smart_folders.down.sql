ALTER TABLE saved_searches
    DROP CONSTRAINT IF EXISTS saved_searches_workspace_pairing_check,
    DROP CONSTRAINT IF EXISTS saved_searches_tree_visibility_check,
    DROP COLUMN IF EXISTS smart_folder_at,
    DROP COLUMN IF EXISTS icon,
    DROP COLUMN IF EXISTS workspace_id,
    DROP COLUMN IF EXISTS tree_visibility,
    DROP COLUMN IF EXISTS is_smart_folder;
DROP INDEX IF EXISTS idx_saved_searches_smart_tree;
