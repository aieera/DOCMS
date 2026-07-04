-- LDAP group → role (§8). A group mapping already grants a DMS group; this
-- lets it ALSO grant a SeDoc role, so an AD group can promote its members
-- (e.g. AD "Domain Admins" → role admin) instead of every LDAP user being a
-- plain member. Nullable + additive: existing group-only mappings are
-- unaffected (their role contribution is "none").
ALTER TABLE ldap_group_mappings
    ADD COLUMN IF NOT EXISTS dms_role TEXT
        CHECK (dms_role IS NULL OR dms_role IN ('owner', 'admin', 'member', 'guest'));
