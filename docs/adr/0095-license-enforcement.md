# ADR 0095 — License enforcement (design plan)

Status: Accepted (design only — no enforcement code shipped)
Date: 2026-05-18
Supersedes: —
Related: §13.7 of the blueprint; ADR 0094 (alt-DB adapter design plan, same scoping pattern)

## Context

VaultDMS today runs in unlicensed mode. There is no JWT signer, no validator
middleware, no seat-counting, no feature-flag gating, no grace-period state
machine. Every tenant deployment is effectively "developer mode" from the
license system's perspective. This is fine for the present open dev cycle, but
the blueprint §13.7 requires a real enforcement layer before the v1 cut so
prod customers can be billed on seats and feature tiers, and so trial /
evaluation deployments expire gracefully instead of silently running forever.

This ADR is the **design plan**, not the implementation. The same shape we
used for ADR 0094 (alt-DB adapters): the design is committed, future
implementation work picks it up phase-by-phase. Implementing the full thing
in one session is multi-week work and would be unsafe to ship without a real
threat-model review of the signer-key infrastructure first.

## What is shipped now

1. This ADR (the design plan).
2. A `GET /api/v1/admin/tenant/license` backend stub that returns an honest
   `unlicensed_dev_mode` response — no JWT validation, no enforcement, just
   the schema the future page will consume.
3. A `/admin/tenant/license` admin page that surfaces today's state honestly
   ("running unlicensed; no enforcement active") and renders the same data
   model the future page will use, so the UI shell exists.

## What is **not** shipped now

- No JWT signing tool (`cmd/license-gen`).
- No validator middleware in `pkg/middleware`.
- No hourly background validator.
- No feature-flag gate at any service boundary.
- No seat-counting integration with the users table.
- No grace-period state machine.
- No read-only degraded mode wiring.

All of the above is scoped in this ADR as future work. None of it is wired up
in the codebase today.

## License JWT — claim schema (future)

```json
{
  "iss": "vaultdms-bd-ops",          // hard-coded issuer
  "sub": "<tenant_id_uuid>",         // who the license is for
  "iat": 1715000000,
  "exp": 1746604800,
  "tenant_name": "Acme Corp",
  "seat_limit": 250,
  "feature_flags": {
    "esign":      true,
    "mcp":        true,
    "ipaas":      false,
    "intel_llm":  true,
    "connectors": ["google_workspace", "salesforce"],
    "regions":    ["us-east-1", "eu-west-1"]
  },
  "issued_to":   "admin@acme.example",
  "issued_by":   "bd-ops@vaultdms",
  "grace_days":  30
}
```

Signed with `RS256` using a signing key held by BD ops. Public key bundled
with every service binary at build time so the validator does not need
network access to verify.

## Signing tool (future)

`cmd/license-gen` — Go binary, takes a YAML input describing the tenant and
emits a signed JWT. Run only by BD ops with the private key.

```
$ license-gen --tenant-id <uuid> --tenant-name "Acme Corp" \
    --seat-limit 250 --expires 2027-04-19 --features esign,mcp,intel_llm \
    --out acme.license.jwt
```

Key custody: signing key in 1Password / HashiCorp Vault, never on developer
machines. `cmd/license-gen` is gated behind a CI workflow with manual approval
so individual engineers cannot mint licenses.

## Validator (future)

New package `pkg/license`:

- `pkg/license/validator.go` — verifies signature with the bundled public key
  (no network), checks `exp`, returns parsed claims or an error.
- `pkg/middleware/RequireLicense.go` — startup hook that loads the license
  JWT from `VAULTDMS_LICENSE_JWT` env or `/etc/vaultdms/license.jwt` file,
  validates, caches claims, and re-validates every hour from a background
  goroutine.
- Failure modes:
  - **No license loaded** → service starts in `unlicensed_dev_mode` (today's
    state). Allowed in dev / on-prem evaluation; blocked in prod via a
    `VAULTDMS_REQUIRE_LICENSE=true` env knob.
  - **Signature invalid / tampered** → service refuses to start. Fail-closed.
  - **Expired ≤ grace_days ago** → degraded mode (read-only). Writes return
    `423 Locked` with a body pointing at the renewal flow.
  - **Expired > grace_days ago** → service refuses to start. Fail-closed.

## Feature-flag gates (future)

Each service exposes a `License.HasFeature(name)` helper. Specific gates by
service:

- `services/signature` → check `esign` before accepting envelope creation.
- `services/mcp-server` → check `mcp` before responding to `initialize`.
- `services/connector` → check `connectors` list contains the requested
  provider before starting OAuth.
- `services/document` → check `regions` contains the upload's target region
  before initiating storage.
- `services/intelligence` → check `intel_llm` before routing LLM calls.

A gate failure returns `403 license_feature_disabled` with the missing flag
name in the body so the frontend can render a "upgrade your plan" message
instead of a generic error.

## Seat counting (future)

Background job in the auth service runs every 5 minutes:

```sql
SELECT COUNT(*)
  FROM users
 WHERE tenant_id = current_tenant_id()
   AND status = 'active';
```

If the count exceeds `seat_limit`, new user creation is blocked (`423
seat_limit_reached`) but existing users continue to function. This keeps
license overage from being a hard outage — admins get the warning and 7-day
window to reduce seats before harder enforcement.

## Grace-period state machine (future)

Three states, all derived from `exp - now()`:

```
  active  ──── exp - now > 0 ────────────────────► active
  active  ──── 0 ≥ exp - now ≥ -30d ──────────────► grace
  active  ──── exp - now < -30d ──────────────────► expired (refuse to start)

  grace   ──── new valid JWT loaded ──────────────► active
  grace   ──── exp - now < -30d ──────────────────► expired
```

In `grace`, the service:
- Allows reads (`GET`, `OPTIONS`, `HEAD`).
- Blocks writes (`POST`, `PUT`, `PATCH`, `DELETE`) with `423 Locked`.
- Emits `dms.license.expired_grace.v1` event every hour for monitoring.
- Frontend shows the read-only banner (already wired in this ADR's UI stub).

## Frontend — already shipped (stub)

`/admin/tenant/license` reads `GET /api/v1/admin/tenant/license` and displays:

- Today's response: `{"status": "unlicensed_dev_mode", ...}` with an honest
  amber banner explaining no enforcement is active.
- Future response shape: `{status, tenant_name, seat_limit, seats_used,
  feature_flags, expiry, days_remaining}`. The future page will reuse the
  same component, swapping the banner from "unlicensed dev" to either
  "active" / "60-day warning" / "30-day warning" / "7-day urgent" / "grace
  read-only" based on `days_remaining`.

## Phased rollout (future)

| Phase | Duration | Deliverable |
|---|---|---|
| 1 | 2 weeks | `cmd/license-gen` + key custody runbook + signed test license |
| 2 | 2 weeks | `pkg/license` validator + `pkg/middleware/RequireLicense` + startup wiring in every service |
| 3 | 1 week | Feature-flag gates at service boundaries + per-service tests |
| 4 | 1 week | Seat counting + grace-period state machine + read-only mode wiring |
| 5 | 3 days | Frontend page upgrade from stub to real (banner system, countdown, read-only banner) |

Total: ~6-7 weeks focused. Will be its own ADR(s) per phase when implementation
starts. Each phase has its own threat model — particularly Phase 1 (signer
key custody) and Phase 4 (read-only enforcement under concurrent writes).

## Why this is a plan, not an implementation

Same reasons as ADR 0094:

1. **Threat model first.** The signer-key custody design needs review by
   someone with sec-eng background before keys are minted. Shipping the
   signer tool before that review is reckless — a leaked key means every
   tenant can mint themselves an unlimited license.
2. **Single-session full build = security regression.** A half-built
   enforcement layer is worse than no enforcement: it implies a guarantee
   that does not hold. Better to ship nothing and label it honestly than
   to ship a validator that passes for a stolen JWT.
3. **No production customers yet** with the license requirement. The honest
   "unlicensed dev mode" today matches reality.

## Open questions deferred to implementation

- Whether the public key is bundled in the binary (simpler, but key rotation
  requires a release) or fetched from a public endpoint at startup (more
  flexible, but adds a network dependency at boot).
- Whether grace-period read-only mode applies per-service or only at the
  gateway. Per-service is safer (defense in depth) but more code.
- Whether seat-counting should include service accounts and SCIM-provisioned
  users.
- Whether feature flags can be downgraded mid-tenant (e.g. a tenant drops
  the `esign` flag — what happens to in-flight envelopes?).
