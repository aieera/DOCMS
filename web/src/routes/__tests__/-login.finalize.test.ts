// H-1: pin the empty-tenant_id guard. Both the password and the
// passkey login paths in login.tsx route through finalizeLoginResult;
// regressing this helper would let an empty-tenant_id user reach the
// auth store, which strips X-Auth-Tenant-ID from every follow-up
// request and turns the session into silent 401s.
import { describe, expect, it } from 'vitest'
import {
  finalizeLoginResult,
  FINALIZE_TENANT_MISSING_MESSAGE,
} from '@/lib/finalizeLogin'
import type { User } from '@/types/api'

const baseUser: User = {
  id: '00000000-0000-0000-0000-000000000001',
  tenant_id: 'aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa',
  email: 'admin@acme.local',
  display_name: 'Admin',
  role: 'owner',
  mfa_enabled: false,
}

describe('finalizeLoginResult — H-1 guard', () => {
  it('rejects undefined user', () => {
    expect(finalizeLoginResult(undefined)).toEqual({ ok: false, reason: 'missing_user' })
  })

  it('rejects null user', () => {
    // Server returning `null` in JSON is type-incompatible with User
    // but happens in practice — backend bug, network proxy stripping
    // body, etc. The guard must catch it.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any -- simulating a wire-shape that the User type forbids
    expect(finalizeLoginResult(null as unknown as any)).toEqual({ ok: false, reason: 'missing_user' })
  })

  it('rejects a user whose tenant_id is the empty string', () => {
    // tenant-fallback: doc-reference
    // This is the exact regression the audit named: `user.tenant_id ?? ''`
    // would have admitted this row.
    expect(finalizeLoginResult({ ...baseUser, tenant_id: '' })).toEqual({
      ok: false,
      reason: 'missing_tenant',
    })
  })

  it('rejects a user with no tenant_id property at all', () => {
    const { tenant_id: _omit, ...userSansTenant } = baseUser
    void _omit
    expect(finalizeLoginResult(userSansTenant as User)).toEqual({
      ok: false,
      reason: 'missing_tenant',
    })
  })

  it('rejects a user with tenant_id === null (JSON null on the wire)', () => {
    expect(
      finalizeLoginResult({
        ...baseUser,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any -- simulating server payload where the field is JSON null
        tenant_id: null as unknown as any,
      }),
    ).toEqual({ ok: false, reason: 'missing_tenant' })
  })

  it('accepts a user with a non-empty tenant_id and echoes both back', () => {
    const res = finalizeLoginResult(baseUser)
    expect(res).toEqual({
      ok: true,
      user: baseUser,
      tenantId: baseUser.tenant_id,
    })
  })

  it('exports a stable copy string so the route + future i18n stay in sync', () => {
    expect(FINALIZE_TENANT_MISSING_MESSAGE).toContain('tenant')
    expect(FINALIZE_TENANT_MISSING_MESSAGE.length).toBeGreaterThan(0)
  })
})
