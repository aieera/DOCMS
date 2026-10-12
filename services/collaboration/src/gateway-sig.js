// X-Gateway-Signature handling, mirroring pkg/middleware/gatewaysig.go:
// the header carries the shared secret SEDOC_GATEWAY_SECRET verbatim, and
// SEDOC_GATEWAY_SECRET_PREV is accepted alongside it during a rotation.
// Secrets are read per call so a test (or a future hot reload) sees the
// current environment.

import { createHash, timingSafeEqual } from 'node:crypto'

export const GATEWAY_SIGNATURE_HEADER = 'X-Gateway-Signature'

function currentSecret() {
  return process.env.SEDOC_GATEWAY_SECRET || process.env.VAULTDMS_GATEWAY_SECRET || ''
}

function acceptedSecrets() {
  const out = []
  const cur = currentSecret()
  if (cur) out.push(cur)
  const prev = process.env.SEDOC_GATEWAY_SECRET_PREV || ''
  if (prev && prev !== cur) out.push(prev)
  return out
}

// Hash both sides to a fixed length so timingSafeEqual never sees
// different-length buffers (which would leak the secret length).
const digest = (s) => createHash('sha256').update(String(s), 'utf8').digest()

/**
 * True when `value` equals the current or previous gateway secret.
 * Constant-time per candidate; an empty value or an unconfigured secret
 * never verifies.
 */
export function verifyGatewaySignature(value) {
  if (typeof value !== 'string' || value === '') return false
  const got = digest(value)
  let ok = false
  for (const secret of acceptedSecrets()) {
    if (timingSafeEqual(got, digest(secret))) ok = true
  }
  return ok
}

/** Headers for an outbound call to a backend behind RequireGatewaySignature. */
export function gatewayHeaders() {
  const secret = currentSecret()
  return secret ? { [GATEWAY_SIGNATURE_HEADER]: secret } : {}
}
