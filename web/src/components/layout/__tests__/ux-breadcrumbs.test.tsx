import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const route = vi.hoisted(() => ({ pathname: '/workspaces/ws-1/documents/doc-1' }))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ to, children, ...r }: { to: string; children: React.ReactNode } & Record<string, unknown>) => <a href={to} {...r}>{children}</a>,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: route.pathname } }),
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

describe('Breadcrumbs — home crumb', () => {
  beforeEach(() => {
    route.pathname = '/workspaces/ws-1/documents/doc-1'
  })

  it('labels the first crumb "Home" with a visible house icon, linking to the dashboard', () => {
    renderWithClient()
    const home = screen.getByRole('link', { name: 'Home' })
    expect(home).toHaveAttribute('href', '/')
    // Visible text (not sr-only) next to the icon.
    const label = screen.getByText('Home')
    expect(label).not.toHaveClass('sr-only')
    expect(home.querySelector('svg')).toHaveClass('lucide-home')
    expect(home).toHaveClass('text-muted-foreground')
    expect(home).not.toHaveAttribute('aria-current')
  })

  it('is the current page, in the foreground colour, on the dashboard', () => {
    route.pathname = '/'
    renderWithClient()
    const home = screen.getByRole('link', { name: 'Home' })
    expect(home).toHaveAttribute('aria-current', 'page')
    expect(home).toHaveClass('text-foreground', 'font-semibold')
    expect(home).not.toHaveClass('text-muted-foreground')
  })
})
