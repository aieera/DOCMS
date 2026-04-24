# ADR 0032 — NATS event subject namespacing

- **Status:** Accepted · 2026-04-24
- **Closes:** T-D-9.
- **Related:** ADR 0021 (who emits which domain event), ADR 0031
  (internal-auth plane), the JetStream stream definitions in
  `pkg/events/` and `deploy/helm/vaultdms/templates/nats/`.

## Context

Subjects arrived by convention rather than spec. Two shapes drifted
into production:

1. **Domain events.** `dms.<aggregate>.<event>.v1` — e.g.
   `dms.document.version.uploaded.v1`, `dms.acknowledgement.acknowledged.v1`,
   `dms.auth.login_failed.v1`. One aggregate per second-level
   segment; action verb(s) in the middle; `.v{n}` suffix.

2. **Notification fan-out.** `dms.notify.<source>.<event>.v1` — e.g.
   `dms.notify.acknowledgement.campaign.created.v1`. The
   notification service binds a wildcard (`dms.notify.>`) consumer
   that expects a `DeliveryPayload` shape (tenant, user_ids, title,
   body, resource_type, resource_id). Domain-event consumers would
   not know what to do with this payload.

Without the `dms.notify.*` carve-out, downstream audit/search/etc
consumers that `dms.>`-subscribe would see delivery payloads as
malformed domain events. T-D-9 called out the hygiene risk and asked
for a lint rule + documentation.

## Decision

Two namespaces, documented as first-class shapes.

### 1. Domain events

- **Shape:** `dms.<aggregate>.<event>.v<n>` (lowercase, dot-separated).
- **Payload:** an aggregate-specific schema. Not `DeliveryPayload`.
- **Examples:**
  - `dms.document.version.uploaded.v1`
  - `dms.acknowledgement.acknowledged.v1`
  - `dms.signature.profile.orphan_swept.v1`
- **Ownership:** the aggregate owner emits. ADR 0021 is the
  authoritative ownership table.
- **Versioning:** bump `v{n}` on breaking payload change; keep both
  versions live until every consumer migrates.

### 2. Notification fan-out

- **Shape:** `dms.notify.<source>.<event>.v<n>`.
- **Payload:** the `notification-service/internal/model.DeliveryPayload`
  shape — `tenant_id`, `user_ids`, `type`, `title`, `body`,
  `resource_type`, `resource_id`. Any other shape is a bug.
- **Consumer:** only `services/notification` subscribes here
  (`dms.notify.>`); no other service should.
- **When to use:** when a domain event should also cause a
  user-visible notification. Emit BOTH — a `dms.<aggregate>.*.v1`
  domain event AND a `dms.notify.<aggregate>.*.v1` fan-out row.

### 3. JetStream + reserved prefixes

- `dms.>` is the cluster-wide catch-all; only in-cluster admin tools
  subscribe (debug tail, audit export).
- `_INBOX.*` / `$JS.*` / `$SYS.*` belong to NATS itself — never use.
- `dms.test.*` is reserved for unit-test emitters — never emit from
  prod code.

## Enforcement

`scripts/lint-nats-subjects.sh` walks every Go source file under
`services/` and `pkg/`, extracts every `dms.*` subject literal, and
flags violations:

- Subjects not matching `^dms\.([a-z][a-z0-9_]*\.)+v[0-9]+$` (domain
  events) AND not matching `^dms\.notify\.([a-z][a-z0-9_]*\.)+v[0-9]+$`
  (notify fan-out).
- Wildcards outside allow-listed consumer call sites.

Wired into CI via a new `lint-nats-subjects` job in `.github/workflows/ci.yml`.
Local operator: `bash scripts/lint-nats-subjects.sh`.

## Consequences

- Adding a new domain subject is mechanical; the linter catches typos
  (e.g. missing `.v1`, uppercase letters, stray `/`).
- Adding a new notification requires consciously using the
  `dms.notify.*` prefix AND the DeliveryPayload shape. The linter
  checks the prefix; payload shape is covered by the notification
  service's consumer test which rejects invalid payloads.
- Existing subject strings already comply. The linter is opening as
  clean; it's the regression guard that matters.

## Alternatives considered

1. **Unify everything under `dms.<aggregate>.<event>.v1`.** Removes
   the carve-out but forces every domain event handler to also know
   the DeliveryPayload shape — or duplicate the fan-out inside each
   aggregate. The two-shapes approach keeps concerns separated.
2. **Per-service subject prefixes (`vaultdms.<svc>.*`).** Larger churn
   and doesn't help the "which events are mine to consume" question.
3. **Typed subject registry in Go.** Considered; deferred until a
   second source of truth (e.g. proto-generated) exists — a Go-only
   registry would drift from the JetStream stream definitions.
