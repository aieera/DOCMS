# Remediation 18a — Wave 11.4: DSR verification-token round-trip

**Date:** 2026-04-18
**Wave:** 11.4 · closes ADR 0024 §6 ("honor system") gap.

## What shipped

### Backend — mint endpoint

[services/document/internal/handler/dsr_verify.go](../../../services/document/internal/handler/dsr_verify.go)
mounts `POST /api/v1/privacy/verify/request-token { subject_email }`:

1. Generates a 32-byte crypto/rand token (64 hex chars).
2. Stores SHA-256(token) in Redis at
   `dsr:verify:{tenant_id}:{lower_trim(email)}` with 24h TTL.
3. Emits outbox event `dms.notify.dsr_verify.v1` carrying the
   plaintext token in the payload. The notification service's
   `dms.notify.>` consumer delivers in-app today; SMTP wires in
   Wave 12.
4. Response does **not** include the token — it's only visible to
   the subject via the notification channel.

Exported `HashDSRToken` + `DSRTokenRedisKey` so the workflow
activity re-derives the same hash and key (identical trim + lower
rules).

### Backend — workflow verification

[services/workflow/internal/activities/dsr.go](../../../services/workflow/internal/activities/dsr.go)
— new activity `VerifyDSRToken(tenantID, email, token)`:

- Hashes the presented plaintext and `hmac.Equal`-compares to the
  Redis-stored digest.
- On match, DELs the key so tokens are single-use.
- On miss / TTL expiry, returns a typed error that the workflow
  converts to `verify-mismatch` in the ledger + `failed` status.

[dsr.go EraseWorkflow](../../../services/workflow/internal/workflows/dsr.go)
replaces the "honor system" branch with a real
`VerifyDSRToken` activity call. PII is never touched when
verification fails.

### Wiring

- `Activities.Redis *redis.Client` field added in
  [activities.go](../../../services/workflow/internal/activities/activities.go).
  Optional — nil-safe fallback returns a typed error.
- Both worker + server main pass `rdb` when constructing activities.
- Document-service main registers the new handler on the shared
  `/api/v1/privacy/*` mux.

### Frontend

- [privacy.ts](../../../web/src/api/privacy.ts) — typed
  `requestDSRToken(subjectEmail)` helper.
- [privacy.tsx](../../../web/src/routes/_authenticated/admin/privacy.tsx)
  shows a "Request verification token" button + help text when the
  request type is `erase`. Admin triggers the email on behalf of
  the subject; the subject then reads the token from their
  notifications feed and pastes it into the existing token input.

### Tests

- 5 new handler tests: missing headers 401, missing email 400,
  malformed email (no `@`) 400, HashDSRToken determinism, Redis
  key canonicalisation (trim + lower).
- New workflow test `TestErase_InvalidTokenFails`: verify failure
  → ledger `verify-mismatch` + no PII touch.

```
$ go test ./services/document/internal/handler/... ./services/workflow/...
ok  github.com/aieera/sedoc/services/document/internal/handler  0.191s
ok  github.com/aieera/sedoc/services/workflow/internal/handler  (cached)
ok  github.com/aieera/sedoc/services/workflow/internal/workflows  0.218s
```

Frontend `tsc --noEmit` clean.

## DoD — ADR 0024 §6

| Requirement | Status |
|---|---|
| 32-byte crypto/rand token | ✅ |
| Hashed at rest (never plaintext) | ✅ SHA-256 digest in Redis |
| 24h TTL | ✅ constant `TokenTTL` |
| Single-use redemption (DEL on success) | ✅ |
| Emitted via notification service | ✅ outbox `dms.notify.dsr_verify.v1` |
| EraseWorkflow redeems via activity | ✅ `VerifyDSRToken` |
| Honor-system fallback removed | ✅ |

## Deferred (logged in out-of-scope.md)

- **SMTP path** — notification service currently logs "would send
  email" (pre-existing state). Until Wave 12 wires SES/SendGrid,
  subjects must read the token from their in-app notifications
  feed. Admin UI help text says so.
- **`scripts/check-dsr-token-leak.sh`** CI guard that fails a PR
  logging `.data.token` in cleartext anywhere outside the notify
  subject payload. Wave 13.4.
- **Rate limit** on `/privacy/verify/request-token` — today runs
  through the global ingress rate limit; spammer could mint tokens
  but each expires 24h. Tighten in Wave 13.3.

## Wave 11 scorecard

| Item | Status |
|---|---|
| 11.1 Workflow RLS audit | ✅ |
| 11.2 OPA compliance-officer gate | ✅ |
| 11.3 Dead-code cleanup | ✅ |
| **11.4 DSR verification token** | ✅ this doc |
| 11.5 Redaction endpoint | pending |
| 11.6 Cross-service DSR erase | pending |
| 11.7 Per-region KEK aliases | pending |

## Next prompt

**11.5 — Redaction endpoint.** Spec Wave 8.2 DoD noted "Redaction
blocked on held documents" but no redaction endpoint exists. Ship
`POST /api/v1/documents/{id}/redact` that applies a bbox-shaped
redaction mask over specific pages, refuses when any active hold
binds the document, and emits `dms.document.redacted.v1`.
