-- The duplicate renames are data cleanup and are not reverted.
DROP INDEX IF EXISTS idx_folders_unique_name_per_parent;
