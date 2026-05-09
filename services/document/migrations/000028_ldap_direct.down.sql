-- ADR 0062 — undo LDAP/AD direct bind tables.
DROP TABLE IF EXISTS ldap_sync_history;
DROP TABLE IF EXISTS ldap_group_mappings;
DROP TABLE IF EXISTS ldap_configs;
