import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { rotateScimToken } from '@/api/scim'
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
    // Disabled until the SCIM info has loaded (W-4: no blind rotate).
    await waitFor(() => expect(btn).toBeEnabled())
    expect(btn).toHaveTextContent(/generate token/i)
  })
})

// W-4: rotating revokes the token the IdP is using — one stray click
// broke provisioning with no way back. It now asks first.
describe('SCIM rotate requires confirmation (W-4)', () => {
  beforeEach(() => {
    info = { slug: 'acme', configured: true, sso_active: true, base_path: '/api/v1/scim/v2/acme' }
    vi.mocked(rotateScimToken).mockClear()
  })

  it('opens a confirm dialog and does not rotate until confirmed', async () => {
    wrap()
    await waitFor(() => expect(screen.getByTestId('rotate-token')).toBeEnabled())
    fireEvent.click(screen.getByTestId('rotate-token'))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent(/rotate/i)
    expect(rotateScimToken).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i, hidden: false }))
    await waitFor(() => expect(rotateScimToken).toHaveBeenCalledTimes(1))
  })

  it('cancel leaves the token alone', async () => {
    wrap()
    await waitFor(() => expect(screen.getByTestId('rotate-token')).toBeEnabled())
    fireEvent.click(screen.getByTestId('rotate-token'))
    await screen.findByRole('alertdialog')
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    expect(rotateScimToken).not.toHaveBeenCalled()
  })
})
