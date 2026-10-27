// DEF-01: "Regenerate recovery codes" used to POST /auth/mfa/setup, which
// minted a NEW TOTP secret while MFA stayed on — the user's authenticator
// app stopped working at once (lockout). The page now asks for the current
// 6-digit authenticator code and posts it to /auth/mfa/recovery-codes,
// which changes only the recovery codes. It must never call /auth/mfa/setup.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import type { ReactNode } from 'react'
import i18n from 'i18next'
import { server } from '@/test/mocks/server'
import arCommon from '../../../../../../public/locales/ar/common.json'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
  createFileRoute: () => (opts: Record<string, unknown>) => ({ ...opts }),
}))

import { Route } from '../mfa_.recovery'

const Page = (Route as unknown as { component: () => ReactNode }).component

const NEW_CODES = ['AAAA2222', 'BBBB3333', 'CCCC4444', 'DDDD5555', 'EEEE6666', 'FFFF7777', 'GGGG8888', 'HHHH9999']

let setupCalls = 0
let regenBodies: unknown[] = []

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><Page /></QueryClientProvider>)
}

describe('recovery codes page — regenerate without touching the authenticator', () => {
  beforeEach(() => {
    setupCalls = 0
    regenBodies = []
    server.use(
      http.post('*/api/v1/auth/mfa/setup', () => {
        setupCalls++
        return HttpResponse.json({ secret: 'X', qr_code_uri: 'otpauth://x', recovery_codes: ['NOPE'] })
      }),
      http.post('*/api/v1/auth/mfa/recovery-codes', async ({ request }) => {
        const body = (await request.json()) as { totp_code?: string }
        regenBodies.push(body)
        if (body.totp_code === '654321') {
          // Right code, already-used time-step (replay protection).
          return HttpResponse.json(
            { type: 'TOTP_CODE_USED', message: 'this code was already used; wait for the next code', field: 'totp_code' },
            { status: 400 },
          )
        }
        if (body.totp_code !== '123456') {
          return HttpResponse.json(
            { type: 'INVALID_ARGUMENT', message: 'invalid code', field: 'totp_code' },
            { status: 400 },
          )
        }
        return HttpResponse.json({ recovery_codes: NEW_CODES })
      }),
    )
  })

  it('asks for the 6-digit authenticator code in a labelled numeric one-time-code input', () => {
    renderPage()
    const input = screen.getByLabelText(/authenticator code/i)
    expect(input).toHaveAttribute('inputmode', 'numeric')
    expect(input).toHaveAttribute('autocomplete', 'one-time-code')
    expect(screen.getByRole('button', { name: /generate new recovery codes/i })).toBeDisabled()
  })

  it('posts the code to /auth/mfa/recovery-codes and shows the new codes; never calls /auth/mfa/setup', async () => {
    renderPage()
    fireEvent.change(screen.getByLabelText(/authenticator code/i), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /generate new recovery codes/i }))

    const list = await screen.findByTestId('recovery-codes-list')
    for (const c of NEW_CODES) expect(list).toHaveTextContent(c)
    expect(regenBodies).toEqual([{ totp_code: '123456' }])
    expect(setupCalls).toBe(0)
  })

  it('shows a rejected code inline and keeps the form', async () => {
    renderPage()
    const input = screen.getByLabelText(/authenticator code/i)
    fireEvent.change(input, { target: { value: '000000' } })
    fireEvent.click(screen.getByRole('button', { name: /generate new recovery codes/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/didn.t work|current 6-digit code/i)
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.queryByTestId('recovery-codes-list')).not.toBeInTheDocument()
    expect(setupCalls).toBe(0)
  })

  it('"Regenerate" asks for a fresh code again instead of re-posting the old one', async () => {
    renderPage()
    fireEvent.change(screen.getByLabelText(/authenticator code/i), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: /generate new recovery codes/i }))
    await screen.findByTestId('recovery-codes-list')

    fireEvent.click(screen.getByRole('button', { name: /^regenerate$/i }))
    expect(screen.getByLabelText(/authenticator code/i)).toHaveValue('')
    expect(regenBodies).toHaveLength(1)
    expect(setupCalls).toBe(0)
  })

  // Review M-6: the code the authenticator shows right after sign-in is in
  // the time-step sign-in just used, so the server refuses it as a replay.
  // "That code didn't work" sent users retyping the same code; the page now
  // tells them to wait for the next one.
  it('tells the user to wait for the next code when the code was already used', async () => {
    renderPage()
    const input = screen.getByLabelText(/authenticator code/i)
    fireEvent.change(input, { target: { value: '654321' } })
    fireEvent.click(screen.getByRole('button', { name: /generate new recovery codes/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Wait for the next code from your authenticator app, then try again.')
    expect(alert).not.toHaveTextContent(/didn.t work/i)
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.queryByTestId('recovery-codes-list')).not.toBeInTheDocument()
  })

  describe('in Arabic', () => {
    afterEach(async () => {
      await act(async () => { await i18n.changeLanguage('en') })
    })

    it('shows the wait-for-the-next-code message in Arabic', async () => {
      i18n.addResourceBundle('ar', 'common', arCommon, true, true)
      await act(async () => { await i18n.changeLanguage('ar') })
      renderPage()
      fireEvent.change(screen.getByLabelText(arCommon.settings.recovery.code_label), { target: { value: '654321' } })
      fireEvent.click(screen.getByRole('button', { name: arCommon.settings.recovery.generate }))

      const alert = await screen.findByRole('alert')
      expect(alert).toHaveTextContent('انتظر الرمز التالي من تطبيق المصادقة، ثم حاول مرة أخرى.')
    })
  })
})
