-- Records certification (E3.2): the columns DoD 5015.2 / ISO 15489 require on
-- a declared record beyond the E3.1 base — vital-records designation, a
-- records freeze (distinct from legal hold), and per-record metadata used for
-- mandatory-metadata coverage checks.

ALTER TABLE records
    -- Vital-records program: records essential to continuity of operations.
    ADD COLUMN IF NOT EXISTS vital_record  BOOLEAN NOT NULL DEFAULT false,
    -- Records freeze: halts disposition independent of legal hold. A frozen
    -- record cannot be disposed until unfrozen (checked in records.Dispose).
    ADD COLUMN IF NOT EXISTS frozen        BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS frozen_by     UUID,
    ADD COLUMN IF NOT EXISTS frozen_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS freeze_reason TEXT,
    -- Record metadata (declaring agent, originating org, security class, etc.).
    -- The mandatory-metadata coverage check counts records missing any key a
    -- standard's profile requires.
    ADD COLUMN IF NOT EXISTS metadata      JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Vital + frozen are reported on, so index the active subsets.
CREATE INDEX IF NOT EXISTS idx_records_vital  ON records(tenant_id) WHERE vital_record;
CREATE INDEX IF NOT EXISTS idx_records_frozen ON records(tenant_id) WHERE frozen;
