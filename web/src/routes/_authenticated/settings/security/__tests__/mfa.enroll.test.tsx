// AUTH (CB-4 server half): enrolling an email/SMS factor now sends a code
// and leaves the factor `pending`; it is activated only by
// POST /auth/mfa/{email,sms}/enroll/verify {code}. The settings page must
// walk the user through enrol → verify instead of claiming success.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock('sonner', () => ({ toast: { success: (...a: unknown[]) => toastSuccess(...a), error: (...a: unknown[]) => toastError(...a) } }))

const enrollEmailMFA = vi.fn(async (..._a: unknown[]) => undefined)
const enrollSMSMFA = vi.fn(async (..._a: unknown[]) => undefined)
const verifyEmailEnrollment = vi.fn(async (..._a: unknown[]) => undefined)
const verifySMSEnrollment = vi.fn(async (..._a: unknown[]) => undefined)
vi.mock('@/api/mfa', () => ({
  listMyEnrolledMethods: vi.fn(async () => []),
  enrollEmailMFA: (...a: unknown[]) => enrollEmailMFA(...a),
  enrollSMSMFA: (...a: unknown[]) => enrollSMSMFA(...a),
  verifyEmailEnrollment: (...a: unknown[]) => verifyEmailEnrollment(...a),
  verifySMSEnrollment: (...a: unknown[]) => verifySMSEnrollment(...a),
  disableMFAMethod: vi.fn(),
}))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
  createFileRoute: () => (opts: Record<string, unknown>) => ({ ...opts }),
}))

import { useAuthStore } from '@/store/authStore'
import { Route } from '../mfa'

const Page = (Route as unknown as { component: () => ReactNode }).component

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><Page /></QueryClientProvider>)
}

describe('MFA settings — enrol then verify', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAuthStore.setState({
      user: { id: 'u', email: 'me@x.test', role: 'member', tenant_id: 't' } as never,
      tenantId: 't', isAuthenticated: true, isHydrating: false, hydrationPromise: null,
    })
  })

  it('email: enrol sends a code, then the code activates the factor', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('mfa-enroll-email'))
    await waitFor(() => expect(enrollEmailMFA).toHaveBeenCalledWith('me@x.test'))
    const code = await screen.findByTestId('mfa-verify-code-email')
    fireEvent.change(code, { target: { value: '123456' } })
    fireEvent.click(screen.getByTestId('mfa-verify-email'))
    await waitFor(() => expect(verifyEmailEnrollment).toHaveBeenCalledWith('123456'))
    await waitFor(() => expect(screen.queryByTestId('mfa-verify-code-email')).not.toBeInTheDocument())
  })

  it('sms: enrol then verify', async () => {
    renderPage()
    fireEvent.change(await screen.findByTestId('mfa-sms-phone'), { target: { value: '+15550001111' } })
    fireEvent.click(screen.getByTestId('mfa-enroll-sms'))
    await waitFor(() => expect(enrollSMSMFA).toHaveBeenCalledWith('+15550001111'))
    fireEvent.change(await screen.findByTestId('mfa-verify-code-sms'), { target: { value: '654321' } })
    fireEvent.click(screen.getByTestId('mfa-verify-sms'))
    await waitFor(() => expect(verifySMSEnrollment).toHaveBeenCalledWith('654321'))
  })

  it('a rejected code keeps the verify step open', async () => {
    verifyEmailEnrollment.mockRejectedValueOnce({ response: { status: 400, data: { error: 'invalid code' } } })
    renderPage()
    fireEvent.click(await screen.findByTestId('mfa-enroll-email'))
    fireEvent.change(await screen.findByTestId('mfa-verify-code-email'), { target: { value: '000000' } })
    fireEvent.click(screen.getByTestId('mfa-verify-email'))
    await waitFor(() => expect(toastError).toHaveBeenCalled())
    expect(screen.getByTestId('mfa-verify-code-email')).toBeInTheDocument()
  })

  it('409 METHOD_UNAVAILABLE says the method is not available', async () => {
    enrollEmailMFA.mockRejectedValueOnce({ response: { status: 409, data: { error: 'METHOD_UNAVAILABLE' } } })
    renderPage()
    fireEvent.click(await screen.findByTestId('mfa-enroll-email'))
    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/not available/i)))
    expect(screen.queryByTestId('mfa-verify-code-email')).not.toBeInTheDocument()
  })
})
