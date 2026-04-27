-- Migration 000002 — session hardening (Blueprint §8.1).
--
-- Adds per-tenant session configuration. Until now all four knobs were
-- package-level constants in services/auth/internal/service/service.go;
-- this migration lets tenants override them. Defaults match the previous
-- constants so behavior is unchanged for any tenant that doesn't set
-- an explicit override.
--
-- Binding strictness controls how the auth middleware reacts when a
-- request's IP (/24 for IPv4, /56 for IPv6) or user-agent fingerprint
-- no longer matches the values recorded at login.
--
--   none    = log only (no header, no revocation) — useful for CI tenants
--   warn    = emit X-Session-Warning response header (default)
--   enforce = 401 + revoke session (high-security tenants)

ALTER TABLE organizations
    ADD COLUMN session_ttl_hours          INT  NOT NULL DEFAULT 24
        CHECK (session_ttl_hours BETWEEN 1 AND 168),
    ADD COLUMN session_sliding_minutes    INT  NOT NULL DEFAULT 60
        CHECK (session_sliding_minutes BETWEEN 5 AND 1440),
    ADD COLUMN session_absolute_max_days  INT  NOT NULL DEFAULT 7
        CHECK (session_absolute_max_days BETWEEN 1 AND 30),
    ADD COLUMN concurrent_session_limit   INT  NOT NULL DEFAULT 5
        CHECK (concurrent_session_limit BETWEEN 1 AND 50),
    ADD COLUMN session_binding_strictness TEXT NOT NULL DEFAULT 'warn'
        CHECK (session_binding_strictness IN ('none', 'warn', 'enforce'));
