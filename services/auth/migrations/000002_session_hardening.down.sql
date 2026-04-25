ALTER TABLE organizations
    DROP COLUMN IF EXISTS session_binding_strictness,
    DROP COLUMN IF EXISTS concurrent_session_limit,
    DROP COLUMN IF EXISTS session_absolute_max_days,
    DROP COLUMN IF EXISTS session_sliding_minutes,
    DROP COLUMN IF EXISTS session_ttl_hours;
