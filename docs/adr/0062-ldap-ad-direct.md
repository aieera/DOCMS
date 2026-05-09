# ADR 0062 — LDAP / Active Directory Direct Bind

Date: 2026-05-07
Status: Accepted

## Context

VaultDMS already supports SAML 2.0 and OIDC SSO (ADRs 0040, 0041)
plus SCIM 2.0 provisioning (ADR 0042). For enterprises that run a
self-hosted directory but no SSO IdP — common in regulated
mid-market deployments — those flows don't help: there's nothing
in front of AD to translate to SAML/OIDC, and SCIM only covers
provisioning, not authentication.

Blueprint §13.5 calls for a fourth identity surface: **direct LDAP
bind**, where the auth service authenticates the user against the
tenant's LDAP/AD server itself.

Three concrete needs:

1. **Login flow.** On `POST /auth/login`, if the tenant has an
   LDAP config marked active, the service binds as the user with
   the supplied password against the directory. If the bind
   succeeds, a local user is upserted (or the existing one
   reused), groups are synced from the directory, and a session
   is issued.
2. **Group sync.** Group memberships drift between logins.
   A scheduled job pulls memberships every 15 min and applies the
   admin-defined AD-group → DMS-group mapping. Real-time
   persistent search is supported where the directory advertises
   it (RFC 3672 / AD `LDAP_SERVER_NOTIFICATION_OID`); otherwise
   the 15-min poll is the floor.
3. **Nested groups.** AD nests heavily. A user's "effective"
   groups must include all transitively-reachable groups, not
   just the direct `memberOf` set.

## Decision

- Library: `github.com/go-ldap/ldap/v3`. STARTTLS by default;
  `ldaps://` scheme accepted. Plain `ldap://` only allowed when the
  config explicitly opts in (`allow_insecure: true`) — surfaced in
  the admin UI as a red banner.
- One config row per tenant in a new `ldap_configs` table:
  - `url` (required), `bind_dn` + `bind_password_encrypted` (the
    service account used for searches), `user_search_base`,
    `user_search_filter` (`(sAMAccountName={username})` for AD,
    `(uid={username})` for OpenLDAP — the form provides defaults),
    `group_search_base`, `group_search_filter`, `nested_groups`
    flag, `allow_insecure` flag, `is_active`, `fallback_to_local`
    flag.
  - Bind passwords are sealed with the same per-tenant KEK that
    wraps blob DEKs (`pkg/crypto/envelope`); the column holds the
    sealed blob, not the secret.
- Group mapping: a sibling table `ldap_group_mappings` maps
  `ldap_group_dn` → `dms_group_id`. Unmapped LDAP groups are
  ignored (no implicit pass-through).
- Sync history: `ldap_sync_history` records every run with
  start/finish timestamps, a counter (`users_synced`,
  `groups_synced`, `errors`), and an optional `error_summary`.
  Last 100 rows retained per tenant; a daily compaction trims older
  rows.
- Nested-group resolution: a single LDAP search using the AD
  matching rule `LDAP_MATCHING_RULE_IN_CHAIN`
  (`memberOf:1.2.840.113556.1.4.1941:`) when the directory advertises
  it (AD ≥ 2008). For OpenLDAP, fall back to recursive resolution
  with a depth cap of 16 and a per-DN visited-set to prevent cycles.
- Connection pooling: a per-tenant pool keyed by `(tenant_id,
  ldap_config_id)`, max-idle 4, max-lifetime 5 min, dial timeout
  5 s, request timeout 10 s. The pool is held in-process; restarts
  drop it. Connections are health-checked with a `WhoAmI` ping
  before reuse.
- Scheduler: a single ticker in `cmd/server/main.go` runs every 15
  min, iterates active tenant configs, and runs sync. A jitter of
  ±60 s avoids the thundering-herd shape when many tenants share
  the same on-the-hour cron alignment.
- Login fallback: when `fallback_to_local=true`, an LDAP bind
  failure with `LDAP_INVALID_CREDENTIALS` falls through to local
  password auth. Any other LDAP error (network, server down)
  returns 503 — admins want to know the directory is broken, not
  silently degrade to a stale local hash.

## Consequences

- We now hold tenant LDAP bind passwords. They go through the
  same envelope encryption that protects blob DEKs; rotating the
  per-tenant KEK rotates these too. Decryption is auth-service-
  local; no other service needs the plaintext.
- We do not relay AD lockout state — three failed binds against AD
  count against the AD account, independent of any local rate
  limit. This is the correct semantics for an org running a
  central lockout policy.
- The `fallback_to_local` toggle is a sharp edge. Default off in
  the form; a help-text note explains that turning it on weakens
  the "AD is the source of truth" property and is mainly useful
  during cutover.
- Nested-group resolution at scale (>5k users in nested groups)
  hits the `LDAP_MATCHING_RULE_IN_CHAIN` path, which AD supports
  natively. The OpenLDAP recursive fallback caps at depth 16
  and 1000 visited DNs per request — beyond that the user gets
  the partial set with a logged warning.
- Persistent search (real-time membership) is opportunistic:
  if the directory rejects the control we silently fall back to
  the 15-min poll. This avoids hard-coding a server matrix.

## Out of scope

- Cross-forest trust. The config models a single directory; multi-
  forest tenants need multiple sso_configs rows (future ADR).
- Kerberos / GSSAPI auth. Future work — direct bind covers the
  blueprint goal for §13.5.
- LDAP-driven user *deletion*. Sync is membership-only; users
  removed from the directory are flagged dormant on next sync but
  not hard-deleted. Hard delete still goes through DSR / admin
  tooling so the audit trail is consistent.
