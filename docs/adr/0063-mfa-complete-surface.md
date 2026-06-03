# ADR 0063 — Complete MFA Surface

Date: 2026-05-08
Status: Accepted

## Context

VaultDMS already supports two MFA factors:

- **TOTP** with bcrypt-hashed recovery codes ([services/auth/internal/service/mfa.go](services/auth/internal/service/mfa.go)),
  enrolled via `POST /auth/mfa/setup`+`/confirm`, verified via
  `POST /auth/mfa/verify` after a password login that flips
  `mfa_required: true`.
- **Passkeys / WebAuthn** as both first-factor and step-up
  (ADR 0061).

§8.1 calls for three more methods plus a per-tenant policy:

1. **SMS** via Twilio Verify (called out as "discouraged but
   supported" — SS7-vulnerable, SIM-swap, etc., but enterprise
   buyers still ask for it).
2. **Email OTP** using the existing transactional-email path
   (SES in prod, SendGrid fallback). No new outbound vendor.
3. **Push** via FCM (Android) / APNs (iOS). The mobile app lands
   in Phase 11.3; for now we ship the *backend surface* —
   device registration, challenge issue, ack consumption — so
   the mobile team has something to integrate against on day 1.
4. **Per-tenant MFA policy**: `disabled` / `optional` / `required` /
   `conditional` (the last gates only specific privileged actions
   the way step-up does today).

A single user must be able to enroll multiple methods at once and
pick the strongest available at login.

## Decision

### Method strength ordering

Strongest → weakest, used at every "pick a factor" decision point:

```
passkey  (5)   phishing-resistant; FIDO2 origin-bound
totp     (4)   shared secret, but no online channel
push     (3)   user presence on a paired device, online
email    (2)   email account compromise → factor compromise
sms      (1)   SS7 / SIM-swap / number portability
```

Recovery codes are not a method but a **break-glass** path with
its own counter (8 single-use codes); they sit outside this
ordering.

### Schema

One row per (user, method) in `user_mfa_methods`:

- `tenant_id, user_id, method`            — composite key
- `status`                                — `pending` | `active` | `suspended`
- `secret_encrypted`                       — TOTP shared secret (envelope-encrypted)
- `phone_e164`                            — SMS destination
- `email`                                 — email-OTP destination (defaults to user.email)
- `last_used_at, created_at, updated_at`
- `failed_count`                          — bumped on each verify failure; 5 = lock until cooldown

Push devices in `user_push_devices`:

- `tenant_id, user_id, device_id`
- `platform`                              — `fcm` | `apns`
- `token`                                 — opaque vendor token
- `label`                                 — "iPhone 15 — Work"
- `created_at, last_used_at, revoked_at`

Tenant policy in `tenant_mfa_policy` (one row per tenant):

- `mode`                                  — `disabled` | `optional` | `required` | `conditional`
- `allowed_methods`                       — TEXT[] subset of {passkey,totp,push,email,sms}
- `conditional_actions`                   — TEXT[] of action scopes that
                                            still demand fresh MFA when
                                            mode=conditional (matches the
                                            step-up scope set from ADR 0061)

### Migration shape

The existing `users.mfa_secret_encrypted` + `users.mfa_recovery_hashes`
columns stay — they're the legacy single-factor TOTP path. New
methods land in `user_mfa_methods`. A user can have a TOTP row in
both places during the transition; the service layer treats
`user_mfa_methods` as authoritative when present and falls back to
the legacy columns otherwise. Backfill happens lazily on next
login.

### Login flow

1. `POST /auth/login` with email + password.
2. If MFA disabled → session issued (unchanged).
3. If MFA enabled:
   - Compute the user's enrolled-method set from `user_mfa_methods`
     (status=active) plus the legacy TOTP if no row exists yet.
   - Return `{ mfa_required: true, mfa_session_token, methods: [...] }`
     with methods sorted strongest-first. The list contains only
     the method names the tenant policy allows.
4. Client picks one and calls one of:
   - `POST /auth/mfa/totp/verify     {code}`            — existing path
   - `POST /auth/mfa/sms/start       {}`                — sends OTP
   - `POST /auth/mfa/sms/verify      {code}`
   - `POST /auth/mfa/email/start     {}`                — sends OTP
   - `POST /auth/mfa/email/verify    {code}`
   - `POST /auth/mfa/push/start      {}`                — emits challenge
   - `POST /auth/mfa/push/verify     {challenge_id, ack}`
   - `POST /auth/mfa/recovery        {code}`            — existing
5. On verify success → session issued; the chosen method's
   `last_used_at` is bumped.

### OTP storage

Email + SMS one-time codes never touch the database. They live in
Redis under
`mfa_otp:{mfa_session_hash}` — 6-digit numeric, 5-minute TTL, max
3 verify attempts. Same `mfa_attempts:` counter enforces the
cooldown the existing TOTP path uses.

### Twilio Verify

We use the **Verify API**, not direct SMS. Twilio owns the OTP
generation, retry / locale localization, and SS7 fraud mitigation.
Per-tenant Twilio credentials in `tenant_secrets` (encrypted under
the tenant KEK) so a tenant can BYO their own subaccount; default
deployment uses the platform Twilio account from
`SEDOC_TWILIO_*` env vars.

### Push (backend-only for now)

Phase 11.3 ships the mobile app. Today we ship:

- `POST /auth/mfa/push/devices` to register a device (mobile
  app calls this on first sign-in once it has an FCM token).
- `POST /auth/mfa/push/start` issues a challenge, writes
  `mfa_push_challenge:{id}` to Redis, emits
  `dms.auth.mfa_push_challenged.v1` to NATS — the notification
  service consumes that and (in Phase 11.3) actually sends the
  FCM/APNs payload.
- `POST /auth/mfa/push/verify` accepts an ack from the device
  signed with a per-device HMAC key; clears the Redis challenge
  and lets the login proceed.

Nothing in the auth service depends on FCM/APNs SDKs directly —
the notification service owns those. This keeps the auth blast
radius tight if FCM is the broken thing.

### Recovery codes

Already 10 codes in the legacy implementation. New default: **8
codes**, 12-character upper-case alphanumeric, bcrypt hashes
stored in `user_mfa_methods.recovery_hashes` (or, when only the
legacy TOTP row exists, in `users.mfa_recovery_hashes` as today).

Using a recovery code consumes it (single-use). When the count
reaches zero the security UI prompts the user to regenerate.

### Step-up integration

Step-up (ADR 0061) currently requires a passkey assertion. This
ADR widens the step-up surface so any *active* MFA method can
satisfy a step-up grant — passkey still preferred, but a TOTP /
push / email / SMS proof within the last 5 minutes also opens
the window. The grant table (`step_up_grants`) gets a
`granted_via` value matching the method name; the existing
`webauthn` value stays as one of the legal values.

## Consequences

- Five total MFA methods (passkey, totp, push, email, sms) plus
  recovery codes. The picker on the login page is a new UI
  surface.
- Per-tenant policy means the login flow must pull
  `tenant_mfa_policy` before deciding whether to skip the MFA
  prompt. One Redis-cached read on the hot path, 60s TTL.
- SMS is never the *only* method we let an enterprise tenant
  enable. Admin-side policy validation rejects an
  `allowed_methods` set of `[sms]` alone — must include at least
  one of {passkey, totp, push}. Surfaced as a banner on the
  policy page.
- Email OTP relies on the same email infra as password resets;
  if email is down, MFA is down for users with no other method.
  Mitigation: any user with `mfa_required` policy must enroll ≥
  2 methods (enforced at policy save time).
- Twilio API costs accrue per send. We rate-limit `sms/start`
  to 3 sends per 15 minutes per user via the existing Redis rate
  limiter.
- Push is incomplete until Phase 11.3 mobile ship. The
  `push/start` route returns 501 unless the tenant has at least
  one registered device, which keeps the UI honest about what's
  callable.

## Out of scope

- Hardware OTP tokens (Yubikey OTP mode, separate from passkey).
  Future ADR if asked.
- WebAuthn-as-second-factor (today it's first-factor or step-up).
- Voice OTP (Twilio Voice Verify variant). Trivially adds in
  the same plumbing if a customer asks.
