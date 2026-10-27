import { describe, it, expect } from 'vitest'
import { isRedirect } from '@tanstack/react-router'
import { Route } from '../register'

// DEF-09: there is no self-service sign-up. Accounts come from invitations,
// SSO/SCIM or admins, so /register only forwards to /login (keeping a
// ?tenant= hint) and renders no form of its own.
const opts = Route.options as unknown as {
  validateSearch: (s: Record<string, unknown>) => { tenant?: string }
  beforeLoad: (ctx: { search: { tenant?: string } }) => unknown
  component?: unknown
}

function redirectFor(search: Record<string, unknown>) {
  try {
    opts.beforeLoad({ search: opts.validateSearch(search) })
  } catch (thrown) {
    expect(isRedirect(thrown)).toBe(true)
    return (thrown as { options: { to?: string; search?: Record<string, unknown>; replace?: boolean } }).options
  }
  throw new Error('/register did not redirect')
}

describe('/register (DEF-09)', () => {
  it('redirects to /login', () => {
    const r = redirectFor({})
    expect(r.to).toBe('/login')
    expect(r.search?.tenant).toBeUndefined()
    expect(r.replace).toBe(true)
  })

  it('preserves ?tenant=', () => {
    expect(redirectFor({ tenant: 'acme' })).toMatchObject({ to: '/login', search: { tenant: 'acme' } })
  })

  it('drops a tenant hint that is not a slug', () => {
    expect(redirectFor({ tenant: '<script>' }).search?.tenant).toBeUndefined()
    expect(redirectFor({ tenant: 42 }).search?.tenant).toBeUndefined()
  })

  it('has no register form', () => {
    expect(opts.component).toBeUndefined()
  })
})
