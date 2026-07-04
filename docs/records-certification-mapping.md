# Records-management certification mapping (E3.2)

Each certifiable standard requirement → the SeDoc mechanism that satisfies it,
and how it is evaluated at runtime. This is the human-readable form of the
machine catalog in [`services/document/internal/records/certification.go`](../services/document/internal/records/certification.go);
the live, per-tenant pass/fail view is **Admin → Data governance → Records
certification** (`GET /api/v1/records/standards/{id}/coverage`).

**Status semantics**
- **attested** — SeDoc provides the mechanism (capability present).
- **pass** — live tenant data confirms the requirement is met.
- **gap** — live data shows the requirement is unmet (action needed).

**Coverage** = (pass + attested) / total requirements.

The **certification evidence pack** (one-click export on the dashboard) bundles
the coverage report, the audit **hash-chain integrity proof**
(`POST /api/v1/audit/verify-integrity` — tamper-evident, signed checkpoints),
and the **accession manifest** (`GET /api/v1/records/accession-export`).

---

## DoD 5015.2

| Req | Requirement | SeDoc mechanism | Check |
|---|---|---|---|
| C2.2.3-fileplan | File plan (categories/series) | `record_categories` tree (E3.1) | pass when ≥1 category |
| C2.2.3-schedules | Retention schedules | `retention_schedules` attached to plan nodes | pass when ≥1 schedule |
| C2.2.6-declare | Record declaration → immutable | `records.Declare` + `DocumentService` gate (`ErrRecordDeclared`, 423) | attested |
| C2.2.10-metadata | Mandatory record metadata | `records.metadata` jsonb + mandatory-key check (`declaring_agent`, `originating_organization`, `security_classification`) | pass when no active record missing keys |
| C2.2.8-cutoff | Cutoff & disposition | records cutoff sweep (`/internal/v1/records/cutoff-sweep`) + `records.Dispose` | pass when every active record has a schedule |
| C2.2.9-certified | Certified disposition | `records.Dispose` (certify) → `disposition_event_id` + `dms.record.disposed.v1` | pass when no disposition lacks a certificate |
| C2.2.12-vital | Vital records program | `records.vital_record` flag + `dms.record.vital_set.v1` | attested |
| C2.2.7-freeze | Freeze / unfreeze | `records.Freeze` / `Unfreeze` (halts disposition; distinct from legal hold) | attested |
| C2.2.11-transfer | Transfer / accession export | `records.AccessionExport` (transfer manifest) + `dms.record.transfer_exported.v1` | attested |
| C2.2.5-audittrail | Audit-trail completeness | `dms.record.*` outbox events (declared/disposed/frozen/unfrozen/vital/metadata/transfer) | attested |
| C2.2.5-auditintegrity | Audit integrity (tamper-evident) | audit hash chain + signed checkpoints (`/audit/verify-integrity`) | attested (proof in evidence pack) |

## ISO 15489

| Req | Requirement | SeDoc mechanism | Check |
|---|---|---|---|
| 9.1-capture | Records capture & declaration | `records.Declare` | attested |
| 9.2-classification | Classification scheme | `record_categories` tree | pass when ≥1 category |
| 9.4-retention | Retention & disposition authorities | `retention_schedules` | pass when ≥1 schedule |
| 9.5-metadata | Records metadata | `records.metadata` + mandatory-key check | pass when no active record missing keys |
| 9.6-access | Access & security | Postgres RLS + policy-service permission checks | attested |
| 9.7-vital | Business continuity (vital records) | `records.vital_record` flag | attested |
| 9.8-disposition | Authorized disposition | `records.Dispose` (certify) + `disposition_event_id` | pass when no disposition lacks a certificate |
| 9.9-audit | Monitoring & auditing | `dms.record.*` events + audit hash chain | attested (proof in evidence pack) |

---

## Events (reused, per contract)

All record actions emit `dms.record.*` via the transactional outbox; the stream
is bound in `pkg/events` and the **audit service subscribes to `dms.record.>`**,
hash-chaining each into the tamper-evident log:

`declared` · `disposed` · `frozen` · `unfrozen` · `vital_set` · `metadata_set` ·
`transfer_exported`.

The disposition certificate handle (`records.disposition_event_id`) is the
`dms.record.disposed.v1` event id — locate it in the audit log and the chain
proof (`/audit/verify-integrity`) certifies it was not altered.
