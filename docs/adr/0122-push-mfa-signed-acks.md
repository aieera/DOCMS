# ADR 0122 — Push-MFA acks must be device-signed (bypass fix)

Date: 2026-07-12
Status: accepted

## Context

`VerifyPushChallenge` treated **any non-empty ack string** as approval.
The MFA session token is held by whoever passed the password step — so
an attacker with a stolen password could start a push challenge and
immediately "approve" it themselves (`ack: "x"`), completing MFA
without possessing any enrolled device. Full MFA bypass for every
push-enrolled account.

The supporting infrastructure already existed and was already
documented as the intended design: `RegisterPushDevice` generates a
per-device ACK key, seals it under the tenant KEK
(`user_push_devices.ack_key_sealed` — the migration comment reads
*"Per-device HMAC key the device signs its push-ack with"*), and
returns the plaintext exactly once for the device to keep in
Keychain/Keystore. Only the verification side was a stub.

## Decision — option (a): real signed acks, now

We implement server-side verification of device-signed acks rather
than disabling push enrollment, because:

1. The signing-key half was already shipped; only verification was
   missing.
2. **No client ever implemented ack sending** (the mobile app has no
   push-MFA ack code). Nobody can be using push-MFA legitimately
   today — any "working" use was the bypass itself. Hardening the
   verify path therefore cannot lock out a single legitimate user,
   which removes the usual argument for the disable-and-migrate
   option.
3. Devices that cannot sign simply cannot pass push-MFA and fall back
   to the user's other factors — the method picker already only offers
   what is enrolled.

## The ack contract (device SDK, ADR 0117 follow-up)

```
ack       = <device_id> "." <signature>
signature = hex( HMAC-SHA256( key = ack_key,
                              msg = <challenge_id> "." <device_id> ) )
```

- `ack_key` is the 64-hex-char secret returned once by
  `POST /auth/mfa/push/devices` (RegisterPushDevice).
- The server recomputes the signature from the sealed key and compares
  in constant time (`hmac.Equal`).

## Verification rules (services/auth `validatePushAck`)

Rejected with `401`, in all cases without consuming the challenge:
- unsigned / malformed acks (including every value the old rubber-stamp
  accepted);
- signatures that don't verify (wrong key, tampered, signed for a
  different challenge or device);
- device rows that are revoked, belong to another user/tenant, or
  don't exist;
- expired challenges (2-minute window, also TTL'd in Redis);
- **replays**: the challenge is deleted only after a signature
  verifies, so the same ack cannot be used twice — and a stream of
  forged attempts cannot burn the legitimate device's window.

## Consequences

- Push-MFA is now secure-but-dormant until the mobile SDK ships the
  signing call (ADR 0117 follow-up); accounts fall back to their other
  enrolled factors, which is exactly the pre-existing real behaviour.
- The tests in `mfa_push_ack_test.go` pin the bypass regression
  (non-empty garbage ack), forged/replayed/expired/revoked rejection,
  and the happy path with a properly signed ack.
