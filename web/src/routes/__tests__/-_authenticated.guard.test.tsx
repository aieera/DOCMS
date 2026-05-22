// Regression tests for the /_authenticated admin guard (C-3).
//
// The audit flagged that `is_platform_admin` was being used as a raw
// value, so a server-returned null / undefined would falsy-pass the
// `!isPlatformAdmin` check and land the user inside the admin shell —
// privilege escalation. The current implementation in
// src/routes/_authenticated.tsx uses strict-boolean comparison
// (`user?.is_platform_admin === true`) and redirects to /login when
// hydration fails or yields no user. These tests pin that behavior so
// the next refactor that touches the guard can't silently regress it.
//
// We don't mount the route; we exercise the beforeLoad callback
// directly through Route.options.beforeLoad with a synthetic location.
// TanStack Router's `redirect()` throws a tagged error object; we
// catch it and assert on `.options.to` rather than calling actual
// navigation.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isRedirect } from '@tanstack/react-router'
import { useAuthStore } from '@/store/authStore'
import type { User } from '@/types/api'

// getCurrentUser is the hydration call the guard fires when the store
// is empty. We mock it to control whether hydration succeeds, fails,
// or returns a user with a particular role/is_platform_admin shape.
vi.mock('@/api/auth', () => ({
  getCurrentUser: vi.fn(),
}))

// Import AFTER the mock declaration so the route picks up the mocked
// getCurrentUser. Route is the value returned by createFileRoute and
// exposes beforeLoad on .options.
import { Route as AuthenticatedRoute } from '../_authenticated'
import * as authApi from '@/api/auth'

const adminLocation = { pathname: '/admin/share-links' } as { pathname: string }
const dashboardLocation = { pathname: '/' } as { pathname: string }

// Minimal valid User. Individual tests override role / is_platform_admin.
const baseUser: User = {
  id: '00000000-0000-0000-0000-000000000001',
  tenant_id: 'aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa',
  email: 'admin@acme.local',
  display_name: 'Admin',
  role: 'owner',
  mfa_enabled: false,
}

async function runGuard(location: { pathname: string }) {
  // beforeLoad's full param object is bigger than the guard needs —
  // we pass the only field the implementation reads.
  return (AuthenticatedRoute.options.beforeLoad as (args: {
    location: { pathname: string }
  }) => Promise<void>)({ location })
}

async function expectRedirectTo(
  run: () => Promise<unknown>,
  expectedPath: string,
) {
  let thrown: unknown
  try {
    await run()
  } catch (e) {
    thrown = e
  }
  if (!thrown) {
    throw new Error(`expected redirect to ${expectedPath}, guard resolved without throwing`)
  }
  expect(isRedirect(thrown)).toBe(true)
  // TanStack stores the redirect target on .options.to.
  expect((thrown as { options?: { to?: string } }).options?.to).toBe(expectedPath)
}

beforeEach(() => {
  // Reset the zustand store to its empty default before each case so
  // tests don't leak through the module-level singleton.
  useAuthStore.setState({
    user: null,
    tenantId: null,
    isAuthenticated: false,
    hydrationPromise: null,
  })
  vi.mocked(authApi.getCurrentUser).mockReset()
})

afterEach(() => {
  useAuthStore.setState({
    user: null,
    tenantId: null,
    isAuthenticated: false,
    hydrationPromise: null,
  })
})

describe('_authenticated guard — null / unauthenticated user', () => {
  it('redirects to /login when the store is empty AND /auth/me throws', async () => {
    vi.mocked(authApi.getCurrentUser).mockRejectedValueOnce(new Error('500'))
    await expectRedirectTo(() => runGuard(adminLocation), '/login')
  })

  it('redirects to /login when /auth/me returns a user with no tenant_id', async () => {
    // The guard treats an empty-tenant_id /auth/me response as auth
    // failure — without tenant_id, every follow-up request would 401
    // server-side. The redirect keeps the user out of the admin
    // shell rather than dropping them into a broken authed UI.
    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce({
      ...baseUser,
      tenant_id: '',
    })
    await expectRedirectTo(() => runGuard(adminLocation), '/login')
  })

  it('still redirects when hitting a non-admin route with no session', async () => {
    vi.mocked(authApi.getCurrentUser).mockRejectedValueOnce(new Error('500'))
    await expectRedirectTo(() => runGuard(dashboardLocation), '/login')
  })
})

describe('_authenticated guard — /admin escalation surface', () => {
  it('redirects to / when user.is_platform_admin is undefined and role is "member"', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'member' /* no is_platform_admin field */ },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expectRedirectTo(() => runGuard(adminLocation), '/')
  })

  it('redirects to / when is_platform_admin is null (server returned JSON null)', async () => {
    useAuthStore.setState({
      user: {
        ...baseUser,
        role: 'member',
        // eslint-disable-next-line @typescript-eslint/no-explicit-any -- simulate a server payload where the field is JSON null
        is_platform_admin: null as unknown as any,
      },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expectRedirectTo(() => runGuard(adminLocation), '/')
  })

  it('redirects to / when is_platform_admin is the literal false', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'member', is_platform_admin: false },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expectRedirectTo(() => runGuard(adminLocation), '/')
  })

  it('redirects to / when is_platform_admin is a truthy non-boolean (e.g. string "true")', async () => {
    // Hardens the strict-equality check: only the literal boolean true
    // should grant access, not any truthy value the server might emit.
    useAuthStore.setState({
      user: {
        ...baseUser,
        role: 'member',
        // eslint-disable-next-line @typescript-eslint/no-explicit-any -- simulate a server bug returning "true" as a string
        is_platform_admin: 'true' as unknown as any,
      },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expectRedirectTo(() => runGuard(adminLocation), '/')
  })

  it('allows the request through when role is "owner"', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'owner' },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expect(runGuard(adminLocation)).resolves.toBeUndefined()
  })

  it('allows the request through when role is "admin"', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'admin' },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expect(runGuard(adminLocation)).resolves.toBeUndefined()
  })

  it('allows the request through when is_platform_admin === true (strict)', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'member', is_platform_admin: true },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expect(runGuard(adminLocation)).resolves.toBeUndefined()
  })

  it('non-admin path skips the admin gate entirely (a member can see /workspaces)', async () => {
    useAuthStore.setState({
      user: { ...baseUser, role: 'member' },
      tenantId: baseUser.tenant_id ?? null,
      isAuthenticated: true,
      hydrationPromise: null,
    })
    await expect(runGuard(dashboardLocation)).resolves.toBeUndefined()
  })
})
