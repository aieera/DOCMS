# Remediation 21d — Wave 14.4: Threat model + pentest plan

**Date:** 2026-04-18
**Wave:** 14.4.

## Recon

Spec §14.4: "STRIDE per trust boundary, known-issues register, pentest
engagement scope, remediation SLA per severity."

Status pre-wave: no consolidated threat model. Security mitigations
existed across the codebase (RLS, envelope encryption, mTLS, CSP,
HMAC webhooks) but weren't catalogued or tied to an explicit threat.

## What shipped

### Threat model

[docs/security/threat-model.md](../../security/threat-model.md) —
STRIDE across 8 trust boundaries:

- B1 Browser/Edge, B2 Edge→API, B3 API→services, B4 Services↔PG,
  B5 Services↔object store, B6 Services↔KMS, B7 External connectors,
  B8 Admin plane.

Each boundary has a threat table: threat description, STRIDE
category, mitigation, **residual risk** (Low/Medium with reasoning).
This last column is the load-bearing one — we're explicit about what
we *haven't* fixed and why.

### Known-issues register

Five entries (KI-01…KI-05) — presigned URL bearer behaviour, no
Shield Advanced, Python-service mTLS gap, no per-tenant search rate
limit, Temporal UI basic-auth-behind-bastion. Each has an ID so PRs
and pentest findings can reference it unambiguously.

### Pentest plan

- **Scope**: app/api/auth hosts, SCIM, connectors, 2 tenants × 2
  users each + API keys.
- **Out of scope**: physical, social engineering, DoS (we own that),
  third-party providers.
- **Remediation SLA**:
  - Critical (RCE, auth bypass, cross-tenant): **72h**
  - High (authZ within tenant, SQLi, stored XSS): **2 weeks**
  - Medium (single-tenant info disclosure): **30 days**
  - Low: **next minor release**
  - Informational: backlog
- **Retest** is in-engagement, per-finding for Critical/High,
  batched for Medium/Low.
- **Disclosure**: playbook for Critical with data impact deferred to
  Wave 14.4b.

### Finding template

[docs/security/pentest-finding-TEMPLATE.md](../../security/pentest-finding-TEMPLATE.md)
— required fields: summary, impact (tied to STRIDE + boundary),
reproduction, affected code, KI link if any, remediation + PR links,
retest result. Each finding gets its own file as `PT-YYYY-NNN.md`.

## DoD

| Requirement | Status |
|---|---|
| STRIDE per trust boundary | ✅ 8 boundaries |
| Known-issues register | ✅ 5 entries, IDs assigned |
| Pentest engagement scope | ✅ |
| Remediation SLA per severity | ✅ |
| First engagement scheduled | 🟡 pre-GA — see below |

## Deferred

- **Schedule first engagement.** Pick a vendor (shortlist: NCC, Bishop
  Fox, Doyensec), get statement of work, book for ~4 weeks before GA.
  Owner: platform lead. Not code.
- **Disclosure playbook** (Wave 14.4b) — customer comms templates +
  CVE coordination when a Critical lands.
- **Threat-model CI check** — a lint rule that fails PRs adding new
  public HTTP endpoints without a threat-model entry. Mechanical once
  the doc is stable; worth it after first real engagement reshapes the
  register. Wave 14.4c.
- **Threat-modelling workshop cadence** — the doc goes stale between
  big releases. Run a 90-min workshop each major release, produce a
  diff against the previous model. Calendar item, not code.

## Wave 14 scorecard

| Item | Status |
|---|---|
| 14.1 Service READMEs | ✅ |
| 14.2 OpenAPI completion | 🟡 rail shipped |
| 14.3 DR runbook + rehearsal | ✅ |
| **14.4 Threat model + pentest plan** | ✅ this doc |
| 14.5 Air-gapped / on-prem packaging | pending |
| 14.6 Release engineering | pending |

## Next prompt

**14.5 — Air-gapped / on-prem packaging.** Spec §14.5: image bundle,
Helm values template, offline-mode docs, license gating.
