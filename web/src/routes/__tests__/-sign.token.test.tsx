// S-13: the remote/mobile signer arrives from /sign/{req}/{signer}?token=…
// The page never forwarded the token, so every record call returned 401.
// S-14: the page must not download / hash / base64 the document — QES
// start takes ids only.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

const getRequest = vi.fn()
const recordSignature = vi.fn()
const startQES = vi.fn()
vi.mock('@/api/signatures', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/signatures')>()),
  getRequest: (...a: unknown[]) => getRequest(...a),
  recordSignature: (...a: unknown[]) => recordSignature(...a),
  startQES: (...a: unknown[]) => startQES(...a),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

// The pad is canvas-driven; replace it with a button that submits a path.
vi.mock('@/components/signatures/SignaturePad', () => ({
  SignaturePad: ({ onSubmit }: { onSubmit: (svg: string) => void }) => (
    <button onClick={() => onSubmit('M0 0 L1 1')}>pad-submit</button>
  ),
  detectDeviceKind: () => 'desktop',
}))

let search: Record<string, unknown> = {}
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
  createFileRoute: () => (opts: Record<string, unknown>) => ({
    ...opts,
    useParams: () => ({ requestId: 'r-1', signerId: 'sg-1' }),
    useSearch: () => search,
  }),
}))

import { Route } from '../_authenticated/sign.$requestId.$signerId'

const route = Route as unknown as {
  component: () => ReactNode
  validateSearch: (s: Record<string, unknown>) => { token?: string }
}
const Page = route.component

const TOKEN = 'a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90'

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <Page />
    </QueryClientProvider>,
  )
}

const fetchSpy = vi.spyOn(globalThis, 'fetch')

beforeEach(() => {
  getRequest.mockReset().mockResolvedValue({
    id: 'r-1', document_id: 'd-1', version_id: 'v-1', status: 'pending', provider: 'internal',
    signers: [{ id: 'sg-1', email: 's@x', name: 'Signer', status: 'pending' }],
    created_at: '', expires_at: '',
  })
  recordSignature.mockReset().mockResolvedValue(undefined)
  startQES.mockReset().mockResolvedValue({ redirect_url: '#', session_id: 's', provider: 'mock', expires_at: '' })
  fetchSpy.mockClear()
})

describe('sign page token (S-13)', () => {
  it('validateSearch keeps a well-formed token and drops anything else', () => {
    expect(route.validateSearch({ token: TOKEN })).toEqual({ token: TOKEN })
    expect(route.validateSearch({ token: 'x"><script>' })).toEqual({ token: undefined })
    expect(route.validateSearch({ token: 42 })).toEqual({ token: undefined })
    expect(route.validateSearch({})).toEqual({ token: undefined })
  })

  it('forwards ?token= to the record call and fetches no document bytes', async () => {
    search = { token: TOKEN }
    renderPage()
    fireEvent.click(await screen.findByText('pad-submit'))
    await waitFor(() => expect(recordSignature).toHaveBeenCalledTimes(1))
    const arg = recordSignature.mock.calls[0][0] as Record<string, unknown>
    expect(arg.token).toBe(TOKEN)
    expect(arg).not.toHaveProperty('doc_hash_sha256')
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('shows an invalid-link message instead of signing when the token is missing', async () => {
    search = {}
    renderPage()
    await waitFor(() => expect(screen.getByText(/signing link is incomplete/i)).toBeInTheDocument())
    expect(screen.queryByText('pad-submit')).not.toBeInTheDocument()
  })

  it('QES start sends ids only (S-14), and needs no link token', async () => {
    search = {}
    renderPage()
    fireEvent.click(await screen.findByTestId('sig-type-qualified'))
    fireEvent.click(screen.getByTestId('qes-start'))
    await waitFor(() => expect(startQES).toHaveBeenCalledTimes(1))
    const arg = startQES.mock.calls[0][0] as Record<string, unknown>
    expect(arg).toMatchObject({ request_id: 'r-1', signer_id: 'sg-1' })
    expect(arg).not.toHaveProperty('document_bytes_b64')
    expect(arg).not.toHaveProperty('signer_email')
    expect(fetchSpy).not.toHaveBeenCalled()
  })
})
