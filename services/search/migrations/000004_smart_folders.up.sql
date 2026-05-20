-- ADR 0100 — Smart folders (saved searches that appear in the
-- folder tree with live auto-update).
--
-- We extend saved_searches in place rather than adding a new table:
-- a smart folder IS a saved search with extra display affordances,
-- and copying rows on promotion would lose the existing subscription
-- and notification history.

ALTER TABLE saved_searches
    ADD COLUMN IF NOT EXISTS is_smart_folder  BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS tree_visibility  TEXT        NOT NULL DEFAULT 'private',
    ADD COLUMN IF NOT EXISTS workspace_id     UUID,                          -- nullable; only meaningful when tree_visibility='workspace'
    ADD COLUMN IF NOT EXISTS icon             TEXT        NOT NULL DEFAULT 'sparkles',
    ADD COLUMN IF NOT EXISTS smart_folder_at  TIMESTAMPTZ;                   -- set when promoted; null otherwise

-- Constrain the visibility enum at the DB layer. Three modes:
--   private    = only the owner sees it in the tree
--   workspace  = anyone with read access to workspace_id sees it
--   public     = visible to every user in the tenant
ALTER TABLE saved_searches
    DROP CONSTRAINT IF EXISTS saved_searches_tree_visibility_check;
ALTER TABLE saved_searches
    ADD CONSTRAINT saved_searches_tree_visibility_check
    CHECK (tree_visibility IN ('private', 'workspace', 'public'));

-- A workspace-scoped smart folder must carry workspace_id; the others
-- must not (it'd be a silent footgun if someone set it on a public
-- folder and then we filtered them out later).
ALTER TABLE saved_searches
    DROP CONSTRAINT IF EXISTS saved_searches_workspace_pairing_check;
ALTER TABLE saved_searches
    ADD CONSTRAINT saved_searches_workspace_pairing_check
    CHECK (
        (tree_visibility = 'workspace' AND workspace_id IS NOT NULL)
     OR (tree_visibility <> 'workspace' AND workspace_id IS NULL)
    );

-- Tree fetch is a hot read: WHERE is_smart_folder AND tenant_id +
-- visibility predicates. Partial index keeps the common case fast.
CREATE INDEX IF NOT EXISTS idx_saved_searches_smart_tree
    ON saved_searches (tenant_id, tree_visibility, workspace_id)
    WHERE is_smart_folder = true;
