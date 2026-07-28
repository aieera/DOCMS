import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ScimPage } from '@/routes/_authenticated/admin/scim'
import type { ScimInfo } from '@/api/scim'

let info: ScimInfo
vi.mock('@/api/scim', () => ({
  getScimInfo: vi.fn(async () => info),
  getScimLog: vi.fn(async () => []),
  rotateScimToken: vi.fn(async () => 'scim_new'),
}))

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><ScimPage /></QueryClientProvider>)
}

describe('SCIM panel — gate rotate on an active SSO config', () => {
  beforeEach(() => { info = { slug: 'acme', configured: false, sso_active: false, base_path: '/api/v1/scim/v2/acme' } })

  it('with no active SSO config: shows a register-IdP notice and no rotate button', async () => {
    wrap()
    expect(await screen.findByText(/register .*identity provider|active SSO config/i)).toBeInTheDocument()
    // link to set up SSO
    expect(screen.getByRole('link', { name: /sso|identity provider/i })).toHaveAttribute('href', '/admin/sso')
    // the doomed rotate/generate button must not be offered
    expect(screen.queryByTestId('rotate-token')).not.toBeInTheDocument()
  })

  it('with an active SSO config: offers the token button (Generate when no token yet)', async () => {
    info = { slug: 'acme', configured: false, sso_active: true, base_path: '/api/v1/scim/v2/acme' }
    wrap()
    const btn = await screen.findByTestId('rotate-token')
    expect(btn).toBeEnabled()
    expect(btn).toHaveTextContent(/generate token/i)
  })
})
