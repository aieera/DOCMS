// DOC3: the zero-trust share endpoints (manifest / stream / telemetry)
// answer 410 {"error":"revoked"|"expired"|"deleted"} once the share is
// gone. The viewer must show a clear "no longer available" card instead
// of the generic "Could not load … manifest 410".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'

vi.mock('react-pdf', () => ({ Document: () => null, Page: () => null }))
vi.mock('@/lib/pdfWorker', () => ({ PDF_WORKER_SRC: '/w.js' }))
vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  createFileRoute: () => (opts: Record<string, unknown>) => ({
    ...opts,
    useParams: () => ({ token: 'tok-1' }),
  }),
}))

import { Route } from '../zt.$token'
import { fetchZTManifest, ZTGoneError } from '@/api/ztShare'

const Page = (Route as unknown as { component: () => ReactNode }).component

function gone(reason: string) {
  return new Response(JSON.stringify({ error: reason }), { status: 410, headers: { 'Content-Type': 'application/json' } })
}

describe('zero-trust viewer — 410 gone (DOC3)', () => {
  let fetchSpy: ReturnType<typeof vi.spyOn>
  beforeEach(() => { fetchSpy = vi.spyOn(globalThis, 'fetch') })
  afterEach(() => fetchSpy.mockRestore())

  it('fetchZTManifest surfaces the 410 reason', async () => {
    fetchSpy.mockResolvedValue(gone('expired'))
    const err = await fetchZTManifest('tok-1').catch((e) => e)
    expect(err).toBeInstanceOf(ZTGoneError)
    expect((err as ZTGoneError).reason).toBe('expired')
  })

  it.each([
    ['revoked', /revoked/i],
    ['expired', /expired/i],
    ['deleted', /deleted/i],
  ])('renders a %s card', async (reason, title) => {
    fetchSpy.mockImplementation(async (input: RequestInfo | URL) =>
      String(input).includes('/manifest') ? gone(reason) : new Response(null, { status: 410 }))
    render(<Page />)
    expect(await screen.findByRole('heading', { name: title })).toBeInTheDocument()
    expect(screen.queryByText(/manifest 410/)).not.toBeInTheDocument()
  })
})
