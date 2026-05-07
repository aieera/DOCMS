# ADR 0070 — Passkeys / WebAuthn

Date: 2026-05-07
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0061 in §8.1; 0061 is taken
by NER Pipeline. Same shift as 0061-0069.)

## Context

The auth service ships TOTP-based MFA today: `POST /auth/login`
returns `mfa_required + mfa_session_token` for users with MFA on,
and the client follows up with `POST /auth/mfa/verify` (TOTP code)
or `POST /auth/mfa/recovery` (single-use recovery code). Recovery
codes are stored as bcrypt hashes on the `users` row.

§8.1 calls out three things TOTP doesn't cover:

1. **Phishing-resistant primary auth.** A passkey assertion is
   bound to the relying-party origin; a phished login page can't
   relay it. TOTP can.
2. **Passkey-as-first-factor.** A user with a synced passkey
   (Apple Keychain / 1Password / Windows Hello) skips the
   password entirely.
3. **Step-up auth.** Sensitive ops — legal-hold release, DSR erase
   approval, quarantine release — should require a *fresh*
   authenticator presence within the last 5 minutes. Today these
   ops only check role.

## Decision

### Library

`github.com/go-webauthn/webauthn`. The de-facto Go implementation;
parses the COSE public key, validates the assertion signature, and
manages session state via a `webauthn.SessionData` blob the caller
persists between begin/finish. We hand the session blob back to the
client wrapped in a one-time HMAC token (no server-side session
store needed).

### Schema

Two new tables (in `services/document/migrations/000027`,
because the `users` table they FK against also lives there):

`webauthn_credentials` — one row per registered authenticator.
| Column | Purpose |
|--------|---------|
| `(tenant_id, credential_id)` PK | credential_id is the authenticator-chosen unique key; re-registering the same key updates rather than duplicates. |
| `public_key` (BYTEA, COSE) | passed back to the lib at assertion time. |
| `sign_count` | bumped per assertion; backward-rolling counter rejects the assertion (cloned authenticator signal). |
| `aaguid`, `transports`, `name` | UI surfaces "Yubikey 5 NFC, added 3 days ago"; `name` is the user-supplied friendly name (REQUIRED — never display a credential without one). |
| `backup_eligible`, `backup_state` | known states for synced passkeys (Apple/Google). The security UI shows a small badge for synced creds. |

`step_up_grants` — fresh-presence window.
| Column | Purpose |
|--------|---------|
| `(tenant_id, user_id, scope, granted_at)` PK | Multiple grants per user (different scopes / different times). |
| `scope` | `''` for all-sensitive-ops; future per-op narrowing. |
| `granted_via`, `credential_id` | Audit + future "must be passkey, not TOTP" gates. |
| `expires_at` | Hard 5-min ceiling; routes can request tighter but not wider. |

### Flows

**Registration** (logged-in user adds a passkey):

```
POST /api/v1/auth/webauthn/registration/begin
  ↳ webauthn.BeginRegistration(user) → options + session
  ↳ return {options, session_token: HMAC(session_blob)}

  client → navigator.credentials.create(options)

POST /api/v1/auth/webauthn/registration/finish
  body: {session_token, attestation_response, friendly_name}
  ↳ verify session_token HMAC + decode session
  ↳ webauthn.FinishRegistration(user, session, response)
  ↳ INSERT webauthn_credentials with friendly_name
  ↳ if user has 0 prior creds: also issue recovery codes
  ↳ return 201
```

**Login** (passkey as primary):

```
POST /api/v1/auth/webauthn/login/begin
  body: {email}
  ↳ load creds for that email's user
  ↳ webauthn.BeginLogin(user) → options + session
  ↳ return {options, session_token}

POST /api/v1/auth/webauthn/login/finish
  body: {session_token, assertion_response}
  ↳ webauthn.FinishLogin(user, session, response)
  ↳ UPDATE webauthn_credentials.sign_count + last_used_at
  ↳ issue session cookie (same cookie the password flow issues)
  ↳ INSERT step_up_grants row (5-min default)
  ↳ return user + session_id
```

**Step-up** (sensitive op):

```
sensitive endpoint hits middleware.RequireStepUp(scope="...")
  ↳ SELECT 1 FROM step_up_grants
     WHERE tenant_id = $tenant AND user_id = $user
       AND (scope = '' OR scope = $scope)
       AND expires_at > now()
     LIMIT 1
  ↳ found  → next handler
  ↳ absent → 401 with header X-Step-Up-Required: webauthn
              client surfaces step-up modal, runs the login flow
              again with a "step-up only" flag (issues a grant
              without issuing a new session cookie)
```

The middleware lives in `pkg/middleware/stepup.go` so any service
can wrap a handler. Document service wires it on:

- `POST /admin/legal-holds/{id}/release`
- `POST /privacy/dsr/{id}/approve-erase`
- `POST /admin/quarantine/{id}/release`

### Recovery

Passkey users still get recovery codes — same shape as TOTP today,
single-use bcrypt-hashed list. Generated on the FIRST passkey
registration (subsequent passkey adds don't re-issue). Recovery
codes do NOT grant step-up; they're an account-recovery escape
hatch only.

### MFA path interaction

A user can have BOTH TOTP and passkeys. Login flow:

1. POST /auth/login with email+password
2. If user has any passkey: response prefers `mfa_required: true,
   mfa_methods: ["webauthn", "totp"]` (UI shows passkey button
   first, TOTP as fallback).
3. POST /auth/webauthn/login/finish OR /auth/mfa/verify completes.

Passkey-only users: POST /auth/webauthn/login/begin without a
prior /auth/login call (passwordless). The login endpoint accepts
either flow.

### What's intentionally NOT in this PR

- Per-op scopes on step_up_grants (just `''` today).
- Conditional UI / passkey-mediation `conditional` requests
  (autofill from credentials picker). The button-based flow ships
  first; conditional UI is a refinement.
- Attestation verification beyond the lib's defaults — we don't
  pin AAGUIDs to a corporate allow-list. A future enterprise
  config can add this.

## Consequences

- **Phishing-resistant primary auth available** for users with a
  passkey. TOTP stays as a fallback per-user choice.
- **5-minute step-up window** raises the bar on irreversible ops
  without making routine work clunky.
- **Two MFA codepaths to keep in sync.** The login response shape
  signals which methods are available; tests must cover the
  combinatorics (passkey only / TOTP only / both / neither).
- **WebAuthn requires HTTPS** in browsers. Dev stack uses
  `localhost` which browsers exempt; prod cloud already runs
  behind TLS. No new infrastructure work.
