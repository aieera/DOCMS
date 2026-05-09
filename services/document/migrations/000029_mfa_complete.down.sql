-- Undo ADR 0063 — drop tables in reverse FK order.
DROP TABLE IF EXISTS tenant_mfa_policy;
DROP TABLE IF EXISTS user_push_devices;
DROP TABLE IF EXISTS user_mfa_methods;
