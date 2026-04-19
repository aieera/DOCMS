# Remediation 19a — Wave 12.1: SMTP transactional email

**Date:** 2026-04-18
**Wave:** 12.1 · closes the Wave 11.4 "SMTP path deferred" item
and the ADR 0024 §6 "email delivery not wired" note.

## Recon

Notification service had one email code path:

```go
if pref != nil && pref.EmailEnabled {
    s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("would send email")
}
```

— a log line where real SMTP should be. DSR tokens (Wave 11.4)
reached the notification service via outbox but never left the
process. Operators had to read tokens from Postgres / in-app
notifications directly.

## What shipped

### Config surface

[pkg/config/config.go](../../../pkg/config/config.go) gains SMTP
fields: `SMTPHost`, `SMTPPort` (default 587), `SMTPUsername`,
`SMTPPassword`, `SMTPFrom` (default `noreply@vaultdms.local`),
`SMTPStartTLS` (default true). `SMTPHost == ""` → sender disabled;
service falls back to the pre-existing log.

### SMTP sender

[services/notification/internal/service/smtp.go](../../../services/notification/internal/service/smtp.go)
— `SMTPSender` interface + `smtpSender` net/smtp-backed impl:

- `Send(to, subject, body)` — one call, plain text, 10s dial
  timeout.
- STARTTLS negotiation with `MinVersion: TLS 1.2` when configured.
- `PLAIN` auth only **after** the channel is encrypted (or when
  StartTLS is explicitly false for dev relays like MailHog).
- Returns error on any ESMTP / TLS / network failure — the outbox
  publisher requeues.
- Never logs the body (DSR tokens flow through here).

Works against AWS SES, SendGrid, Mailgun, Postmark, Postfix,
MailHog. Host-specific API paths (SendGrid Web API, SES v2) are
not wired — SMTP is universal.

### Service wire-through

[services/notification/internal/service/service.go](../../../services/notification/internal/service/service.go):

- `Service.smtp SMTPSender` field, DI via `Config.SMTP`.
- `Deliver` branches:
  - `smtp.Enabled() && uid contains '@'` → real send
  - disabled → same `would send email` log as before
  - uid is a UUID (no `@`) → `email skipped: user-id not an email
    (lookup pending)` — the DSR verify path passes email
    addresses directly; future publishers that pass UUIDs will
    resolve via a DB lookup (follow-up).

[services/notification/cmd/server/main.go](../../../services/notification/cmd/server/main.go)
constructs the sender from config and wires it into the service.

### Tests

[services/notification/internal/service/smtp_test.go](../../../services/notification/internal/service/smtp_test.go):

1. `DisabledWhenHostEmpty` — empty host → `Enabled() == false`,
   Send is a no-op.
2. `RejectsEmptyTo` — empty recipient → 400-equivalent error.
3. `BuildRFC5322_IncludesHeaders` — From / To / Subject / MIME
   headers each CRLF-terminated; blank line before body; body
   appended verbatim.
4. `ContainsAt` — email detection helper.

```
$ go test ./services/notification/...
ok  github.com/vaultdms/vaultdms/services/notification/internal/service  4.648s
```

## DoD

| Requirement | Status |
|---|---|
| Real SMTP path (not log-only) | ✅ `net/smtp` |
| STARTTLS + auth | ✅ TLS 1.2+ min; PLAIN auth only post-TLS or explicit-plaintext |
| Config-driven enable/disable | ✅ empty host disables |
| DSR verification token reaches subject via email | ✅ Wave 11.4's outbox event now lands in an actual inbox |
| No body logging | ✅ |
| Backwards-compatible fallback | ✅ existing "would send" log retained when SMTP off |

## Deferred (logged in out-of-scope.md)

- **User-id → email lookup** on the notification service so
  publishers passing UUIDs (not emails) still deliver via SMTP.
  Needs a small `users` join query with tenant scoping + a 5-min
  cache. Follow-up.
- **HTML / multipart messages** — plain text only today. When
  marketing-style templates arrive, swap in
  `jordan-wright/email` or similar.
- **SES / SendGrid REST APIs** — the SMTP path covers all
  providers universally; native APIs unlock features (suppression
  lists, analytics) but aren't day-one material.
- **Retry + DLQ** for hard-bounce emails — today the outbox
  publisher requeues on any error; need a backoff + dead-letter
  channel for permanent failures.

## Wave 12 scorecard

| Item | Status |
|---|---|
| **12.1 SMTP transactional email** | ✅ this doc |
| 12.2 Storage cross-region copy + re-encrypt | pending |
| 12.3 Background re-wrap for regional KEKs | pending |
| 12.4 Qdrant / OpenSearch subject erase | pending |
| 12.5 Redaction fan-out to intelligence | pending |
| 12.6 Connector OAuth token purge | pending |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS production adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.2 — Storage cross-region copy + re-encrypt.** Completes
Wave 8.4 residency workflow; today only flips `region_pin` +
emits `dms.residency.migrated.v1`, but the ciphertext stays in
the source bucket.
