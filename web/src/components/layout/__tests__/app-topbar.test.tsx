import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'

import { AppTopbar } from '../app-topbar'
import { renderWithProviders } from '@/test/renderWithProviders'
import { useAuthStore } from '@/store/authStore'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children?: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
  useNavigate: () => vi.fn(),
}))
vi.mock('@/api/tasks', () => ({
  taskKeys: { mine: () => ['tasks', 'mine'] },
  listMyTasks: vi.fn().mockResolvedValue({ items: [] }),
}))
vi.mock('@/api/notifications', () => ({
  getUnreadCount: vi.fn().mockResolvedValue({ count: 0 }),
  getNotifications: vi.fn().mockResolvedValue({ items: [], total_count: 0 }),
  markAsRead: vi.fn(),
  markAllRead: vi.fn(),
}))
vi.mock('@/api/savedSearches', () => ({
  listSavedSearches: vi.fn().mockResolvedValue([]),
  createSavedSearch: vi.fn(),
  deleteSavedSearch: vi.fn(),
}))
// Heavy children that have their own suites and no bearing on the
// breakpoint classes asserted here.
vi.mock('../breadcrumbs', () => ({ Breadcrumbs: () => <nav data-testid="breadcrumbs-stub" /> }))
vi.mock('@/components/notifications/NotificationsPanel', () => ({ NotificationsPanel: () => null }))
vi.mock('../theme-toggle', () => ({ ThemeToggle: () => null }))
vi.mock('@/components/shared/LanguageSelector', () => ({ LanguageSelector: () => null }))

beforeEach(() => {
  useAuthStore.setState({
    user: { id: 'u-1', email: 'a@x', role: 'owner', tenant_id: 't-1' } as never,
    tenantId: 't-1',
    isAuthenticated: true,
    isHydrating: false,
    hydrationPromise: null,
  })
})

// The rail appears at the `shell` breakpoint (801px), so the header's
// open-navigation button and breadcrumb/spacer swap must flip at the same
// width — otherwise there is a band with neither a rail nor a menu button.
describe('AppTopbar shell breakpoint', () => {
  it('shows the open-navigation button only below the shell breakpoint', () => {
    renderWithProviders(<AppTopbar onOpenMobileNav={() => {}} />)
    const btn = screen.getByRole('button', { name: /open navigation/i })
    expect(btn).toHaveClass('shell:hidden')
    expect(btn).not.toHaveClass('lg:hidden')
  })

  it('shows the breadcrumbs from the shell breakpoint up and a spacer below it', () => {
    renderWithProviders(<AppTopbar onOpenMobileNav={() => {}} />)
    const crumbs = screen.getByTestId('breadcrumbs-stub').parentElement as HTMLElement
    expect(crumbs).toHaveClass('hidden', 'shell:block')
    expect(crumbs).not.toHaveClass('lg:block')
    const spacer = crumbs.nextElementSibling as HTMLElement
    expect(spacer).toHaveClass('flex-1', 'shell:hidden')
    expect(spacer).not.toHaveClass('lg:hidden')
  })
})
