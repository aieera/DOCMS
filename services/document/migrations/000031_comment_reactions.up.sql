-- ADR 0066 — emoji reactions on comments.
--
-- One row per (tenant, comment, user, emoji) so a user can leave
-- one reaction per emoji per comment but multiple distinct emoji.
-- Toggling on = INSERT ... ON CONFLICT DO NOTHING; toggling off =
-- DELETE matching all four columns.

CREATE TABLE IF NOT EXISTS comment_reactions (
    tenant_id   UUID         NOT NULL REFERENCES organizations(id),
    comment_id  UUID         NOT NULL,
    user_id     UUID         NOT NULL,
    emoji       TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, comment_id, user_id, emoji),
    FOREIGN KEY (tenant_id, comment_id) REFERENCES comments(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, user_id)    REFERENCES users(tenant_id, id)    ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comment_reactions_comment
    ON comment_reactions (tenant_id, comment_id);

ALTER TABLE comment_reactions ENABLE ROW LEVEL SECURITY;
ALTER TABLE comment_reactions FORCE  ROW LEVEL SECURITY;
DROP POLICY IF EXISTS comment_reactions_tenant_isolation        ON comment_reactions;
DROP POLICY IF EXISTS comment_reactions_tenant_isolation_insert ON comment_reactions;
CREATE POLICY comment_reactions_tenant_isolation ON comment_reactions
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY comment_reactions_tenant_isolation_insert ON comment_reactions
    FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
