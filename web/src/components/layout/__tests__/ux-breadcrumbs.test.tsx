import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ to, children, ...r }: { to: string; children: React.ReactNode } & Record<string, unknown>) => <a href={to} {...r}>{children}</a>,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: '/workspaces/ws-1/documents/doc-1' } }),
}))

import { Breadcrumbs } from '../breadcrumbs'

// Breadcrumbs subscribes (enabled: false) to a few React Query caches to
// read workspace/document names the detail pages have already populated
// — it never fires its own fetch, but the hook still needs a client in
// the tree.
function renderWithClient() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <Breadcrumbs />
    </QueryClientProvider>,
  )
}

describe('Breadcrumbs — structural segments', () => {
  it('does not render a link to the routeless /documents segment', () => {
    renderWithClient()
    const dead = screen.queryAllByRole('link').filter((a) => (a as HTMLAnchorElement).getAttribute('href')?.endsWith('/documents'))
    expect(dead).toHaveLength(0)
  })
})
