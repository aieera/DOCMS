-- Revert to the four-role CHECK. Any rows with the new roles must be
-- coerced beforehand or the constraint add will fail.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users
    ADD CONSTRAINT users_role_check
    CHECK (role IN ('owner', 'admin', 'member', 'guest'));
