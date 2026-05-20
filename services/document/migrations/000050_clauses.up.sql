-- ADR 0104 — Clause library + reuse.
-- Phase 1: table + trigger-maintained FTS column. Detection,
-- variations, and approval workflow are explicitly Phase 2-4 work.
--
-- Note: an earlier draft of this migration used a GENERATED ALWAYS AS
-- STORED tsvector column. Postgres refused it because to_tsvector is
-- classified STABLE (not IMMUTABLE) — text-search configurations can
-- theoretically change. Trigger-maintained tsvector is the standard
-- workaround.

CREATE TABLE IF NOT EXISTS clauses (
    tenant_id      uuid        NOT NULL REFERENCES organizations(id),
    id             uuid        NOT NULL DEFAULT gen_random_uuid(),
    name           text        NOT NULL,
    body_text      text        NOT NULL,
    jurisdiction   text        NOT NULL DEFAULT '',
    tags           text[]      NOT NULL DEFAULT '{}',
    version        integer     NOT NULL DEFAULT 1,
    approved_by    uuid,
    approved_at    timestamptz,
    created_by     uuid        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,
    search_tsv     tsvector,
    PRIMARY KEY (tenant_id, id)
);

-- Trigger keeps search_tsv in sync with name + body_text + tags.
-- Cheap on writes (low-thousands of rows per tenant), avoids a
-- second roundtrip on every read.
CREATE OR REPLACE FUNCTION clauses_update_search_tsv() RETURNS trigger AS $$
BEGIN
    NEW.search_tsv := to_tsvector('english',
        coalesce(NEW.name, '') || ' ' ||
        coalesce(NEW.body_text, '') || ' ' ||
        array_to_string(NEW.tags, ' ')
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_clauses_search_tsv ON clauses;
CREATE TRIGGER trg_clauses_search_tsv
    BEFORE INSERT OR UPDATE OF name, body_text, tags ON clauses
    FOR EACH ROW EXECUTE FUNCTION clauses_update_search_tsv();

CREATE INDEX IF NOT EXISTS idx_clauses_search
    ON clauses USING GIN (search_tsv);
CREATE INDEX IF NOT EXISTS idx_clauses_tenant_active
    ON clauses (tenant_id, updated_at DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_clauses_jurisdiction
    ON clauses (tenant_id, jurisdiction)
    WHERE deleted_at IS NULL;

ALTER TABLE clauses ENABLE ROW LEVEL SECURITY;
ALTER TABLE clauses FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS clauses_tenant_isolation ON clauses;
CREATE POLICY clauses_tenant_isolation ON clauses
    USING (tenant_id::text = current_setting('app.current_tenant', true))
    WITH CHECK (tenant_id::text = current_setting('app.current_tenant', true));
