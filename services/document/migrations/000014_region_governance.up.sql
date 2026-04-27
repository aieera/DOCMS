-- Migration 000014 — region governance (Blueprint §9.1 follow-through).
--
-- Three additions, all non-breaking for existing tenants:
--
-- 1. supported_regions — canonical region code table with a geopolitical
--    boundary tag (EU / US / MENA / APAC / OTHER). Referenced by CHECK
--    constraints and by pkg/regionenforcer/boundaries.go (Go mirrors the
--    boundary sets, but this table is the DB-side source of truth for
--    reconciliation queries + admin UI dropdowns).
--
-- 2. organizations.allowed_regions TEXT[] — per-tenant allowlist. NULL
--    means "no restriction" (back-compat for existing orgs). Document
--    creation fails with REGION_VIOLATION when a caller picks a region
--    outside this set.
--
-- 3. organizations.default_region_pin TEXT — per-tenant default the
--    service layer falls back to when neither the request nor the
--    workspace specify a region. New tenants default to me-south-1
--    (brief §9.1); existing tenants backfill to us-east-1 so the
--    residency contract for already-stored documents stays intact.
--
-- DO NOT add a CHECK constraint on documents.region_pin against this
-- table — existing rows may reference a region we're seeding for the
-- first time, and the constraint would need NOT VALID + validate steps.
-- The service layer validates on write; the invariant is enforced
-- on the write path, not at rest.

CREATE TABLE supported_regions (
    code              TEXT PRIMARY KEY,
    display_name      TEXT NOT NULL,
    -- EU | US | MENA | APAC | OTHER. Cross-boundary data movement is
    -- blocked by default; migrations within the same boundary are the
    -- normal residency-migration path.
    boundary          TEXT NOT NULL CHECK (boundary IN ('EU', 'US', 'MENA', 'APAC', 'OTHER')),
    -- Marks regions the platform can actually write to. A region may
    -- stay in this table as 'retired' after capacity is decommissioned
    -- so historical documents still resolve their boundary correctly.
    active            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO supported_regions (code, display_name, boundary) VALUES
    ('us-east-1',      'US East (N. Virginia)',   'US'),
    ('us-west-2',      'US West (Oregon)',        'US'),
    ('eu-west-1',      'EU West (Ireland)',       'EU'),
    ('eu-central-1',   'EU Central (Frankfurt)',  'EU'),
    ('me-south-1',     'Middle East (Bahrain)',   'MENA'),
    ('ap-southeast-1', 'APAC (Singapore)',        'APAC'),
    ('ap-northeast-1', 'APAC (Tokyo)',            'APAC'),
    ('custom',         'Custom / on-prem',        'OTHER');

ALTER TABLE organizations
    ADD COLUMN allowed_regions    TEXT[],
    ADD COLUMN default_region_pin TEXT;

-- Backfill: existing tenants keep us-east-1 (their historical default
-- from 000001). New orgs inserted after this migration will pick up
-- me-south-1 via the DEFAULT below.
UPDATE organizations SET default_region_pin = 'us-east-1'
WHERE default_region_pin IS NULL;

ALTER TABLE organizations
    ALTER COLUMN default_region_pin SET NOT NULL,
    ALTER COLUMN default_region_pin SET DEFAULT 'me-south-1';

CREATE INDEX idx_documents_tenant_region ON documents(tenant_id, region_pin);
