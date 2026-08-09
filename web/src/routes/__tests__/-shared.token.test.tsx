// Public share link with a dud token (BUG-19).
//
// /shared/invalid-token-test-123 used to answer with "Couldn't load
// this link — authentication required", plus a second, identical
// complaint as a toast. Both are wrong for a link whose whole premise
// is that the recipient has no account: there is nothing to
// authenticate, and the page offers no way to.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'

const accessShareLink = vi.fn()
vi.mock('@/api/shareLinks', () => ({
  accessShareLink: (...args: unknown[]) => accessShareLink(...args),
}))

const toastError = vi.fn()
vi.mock('sonner', () => ({ toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn() } }))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  createFileRoute: () => (opts: Record<string, unknown>) => ({
    ...opts,
    useParams: () => ({ token: 'invalid-token-test-123' }),
  }),
}))

import { Route } from '../shared.$token'

const Page = (Route as unknown as { component: () => ReactNode }).component

const httpError = (status: number, error: string) => ({
  response: { status, data: { error } },
})

beforeEach(() => {
  accessShareLink.mockReset()
  toastError.mockReset()
})

describe('public share viewer', () => {
  it('calls an unauthenticated 401 an invalid link, and toasts nothing', async () => {
    accessShareLink.mockRejectedValue(httpError(401, 'authentication required'))

    render(<Page />)

    await waitFor(() => expect(screen.getByText(/isn't valid/i)).toBeInTheDocument())
    expect(screen.queryByText(/authentication required/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/Couldn't load this link/i)).not.toBeInTheDocument()
    expect(toastError).not.toHaveBeenCalled()
  })

  it('says the same for 403 and 404', async () => {
    for (const status of [403, 404]) {
      accessShareLink.mockRejectedValue(httpError(status, 'forbidden'))
      const { unmount } = render(<Page />)
      await waitFor(() => expect(screen.getByText(/isn't valid/i)).toBeInTheDocument())
      unmount()
    }
  })

  it('still distinguishes an expired / view-capped link', async () => {
    accessShareLink.mockRejectedValue(httpError(409, 'link has expired'))
    render(<Page />)
    await waitFor(() =>
      expect(screen.getByText(/no longer accessible/i)).toBeInTheDocument(),
    )
  })
})
