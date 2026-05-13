-- Expand users.role CHECK to include 'viewer' and 'compliance_officer'.
--
-- The admin UI has exposed these two roles since Wave 11.2 (legal-hold +
-- compliance flows) and the auth service's ChangeUserRole handler already
-- accepts them, but the underlying CHECK constraint was still the
-- four-role one from 000001 and rejected every assignment with a 23514
-- check_violation that surfaced as a 400 in the UI. Bringing the schema
-- in line with the application invariants.

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users
    ADD CONSTRAINT users_role_check
    CHECK (role IN ('owner', 'admin', 'member', 'guest', 'viewer', 'compliance_officer'));
