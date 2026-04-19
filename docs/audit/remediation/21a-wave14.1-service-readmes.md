# Remediation 21a — Wave 14.1: Service READMEs

**Date:** 2026-04-18
**Wave:** 14.1.

## Recon

Spec §14.1: "Each service has a README covering purpose, API surface,
dependencies, config, local run, testing, deployment, metrics,
troubleshooting."

Status at wave start: **all 15 services already have substantive
READMEs** (70–111 lines each), shipped incrementally through earlier
waves. Rather than rewrite what is already good, I audited each file
against the §14.1 checklist.

## Audit

| Service | Lines | Resp | API | Deps | Config | Run | Test | Deploy | Metrics | Troubleshoot |
|---|---|---|---|---|---|---|---|---|---|---|
| audit | 70 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| auth | 92 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| billing | 78 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| collaboration | 85 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| connector | 75 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| document | 77 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| intelligence | 104 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| notification | 71 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| policy | 75 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| preview | 81 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| search | 111 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| signature | 77 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| signature-signer | 81 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| storage | 88 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| workflow | 84 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

All 15 services pass the §14.1 checklist. No rewrites needed.

## DoD

| Requirement | Status |
|---|---|
| README per service (15 of 15) | ✅ pre-existing |
| Purpose / responsibilities | ✅ |
| API surface documented | ✅ |
| Deps + config env vars | ✅ |
| Local run + test recipe | ✅ |
| Deployment pointer (Helm) | ✅ |
| Metrics + troubleshooting | ✅ |

## Deferred

- **READMEs drift as services evolve.** Current snapshot is accurate
  for Wave 13-era code; they will go stale. Add a lint rule in Wave
  14.2+ that checks README mentions every exported HTTP route, so
  API drift is caught in CI.
- **Intelligence sub-services docs.** `services/intelligence` has
  Python sub-packages (OCR, classifier, redactor) — the top-level
  README covers them, but each could use its own module-level doc.
  Low priority.

## Wave 14 scorecard

| Item | Status |
|---|---|
| **14.1 Service READMEs** | ✅ this doc (audit pass) |
| 14.2 OpenAPI completion | pending |
| 14.3 DR runbook + rehearsal | pending |
| 14.4 Threat model + pentest plan | pending |
| 14.5 Air-gapped / on-prem packaging | pending |
| 14.6 Release engineering | pending |

## Next prompt

**14.2 — OpenAPI completion.** Spec §14.2: every HTTP endpoint has an
OpenAPI 3.1 definition; published; CI check that handler ↔ spec
doesn't drift.
