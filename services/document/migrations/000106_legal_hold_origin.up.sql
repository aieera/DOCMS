-- Legal-hold schema alignment (review remediation H-8 / DEC-7).
--
-- The in-band lifecycle action apply_hold used to INSERT columns that
-- legal_hold_documents never had (id, hold_name, reason, placed_by, …), so
-- the legal_hold lifecycle state was unreachable. It now writes the real
-- schema — a legal_holds row plus its legal_hold_documents binding — exactly
-- like POST /compliance/holds.
--
-- release_hold must then deactivate the hold the lifecycle action placed and
-- never a compliance-API hold on the same document: those are released only
-- through /compliance/holds/{id}/release with an approver and a reason.
-- Nothing in the existing columns tells the two apart (name, description and
-- matter_reference are caller text; applied_at is transaction-start time), so
-- the origin is recorded. Every pre-existing row came from the compliance
-- API, which the default reflects; that API keeps working unchanged.
ALTER TABLE legal_holds
    ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'compliance';

ALTER TABLE legal_holds DROP CONSTRAINT IF EXISTS legal_holds_origin_check;
ALTER TABLE legal_holds
    ADD CONSTRAINT legal_holds_origin_check CHECK (origin IN ('compliance', 'lifecycle'));
