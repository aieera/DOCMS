// DEF-09: the product has no "Create an account" option — accounts come from
// invitations, SSO/SCIM or admins. The sign-in card must not offer a sign-up
// link, and a ?tenant= hint (e.g. forwarded from the retired /register)
// pre-fills the tenant field.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'

let search: { redirect?: string; tenant?: string } = {}

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>,
  useNavigate: () => vi.fn(),
  createFileRoute: () => (opts: Record<string, unknown>) => ({ ...opts, options: opts, useSearch: () => search }),
}))

import { Route } from '../login'

const Page = (Route as unknown as { component: () => ReactNode }).component
const validate = (Route as unknown as {
  options: { validateSearch: (s: Record<string, unknown>) => { redirect?: string; tenant?: string } }
}).options.validateSearch

describe('login — no self-service sign-up (DEF-09)', () => {
  beforeEach(() => {
    search = {}
  })

  it('offers no "Create an account" link and no link to /register', () => {
    const { container } = render(<Page />)
    expect(screen.queryByText(/create an account/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/new to sedoc/i)).not.toBeInTheDocument()
    expect(container.querySelector('a[href="/register"]')).toBeNull()
  })

  it('pre-fills the tenant from ?tenant=', () => {
    search = { tenant: 'globex' }
    render(<Page />)
    expect(screen.getByLabelText('Tenant')).toHaveValue('globex')
  })

  it('validateSearch keeps a slug-shaped tenant and drops anything else', () => {
    expect(validate({ tenant: 'globex-2' }).tenant).toBe('globex-2')
    expect(validate({ tenant: 'a b' }).tenant).toBeUndefined()
    expect(validate({ tenant: 7 }).tenant).toBeUndefined()
  })
})
