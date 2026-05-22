// finalizeLogin — single chokepoint for "we just got a logged-in user
// back from auth and need to commit it to the store". Lives outside
// the route component so the H-1 invariant (no empty tenant_id ever
// reaches the auth store) is unit-testable without rendering the
// LoginPage with a full router context.
//
// All three success paths in login.tsx (password, MFA verify, passkey)
// route through this. Wave 5 pattern 5 — the guard cannot be forgotten
// on one path because there is only one path.
import type { User } from '@/types/api'

export type FinalizeResult =
  | { ok: true; user: User; tenantId: string }
  | { ok: false; reason: 'missing_user' | 'missing_tenant' }

export function finalizeLoginResult(user: User | undefined | null): FinalizeResult {
  if (!user) return { ok: false, reason: 'missing_user' }
  if (!user.tenant_id) return { ok: false, reason: 'missing_tenant' }
  return { ok: true, user, tenantId: user.tenant_id }
}

// Stable copy so the route + tests agree on the failure surface.
export const FINALIZE_TENANT_MISSING_MESSAGE =
  'Sign-in succeeded but no tenant was assigned. Contact your administrator.'
