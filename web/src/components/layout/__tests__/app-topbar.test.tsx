import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { AppTopbar } from '../app-topbar'
import { ThemeProvider } from '../theme-provider'
import { renderWithProviders } from '@/test/renderWithProviders'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { listMyTasks } from '@/api/tasks'
import { getUnreadCount } from '@/api/notifications'
import { recordRecentSearch } from '@/lib/recentSearches'
import { shortcutLabel } from '@/lib/platform'
import enCommon from '../../../../public/locales/en/common.json'
import arCommon from '../../../../public/locales/ar/common.json'

const { navigateMock, routerState } = vi.hoisted(() => ({
  navigateMock: vi.fn(),
  routerState: { pathname: '/workspaces' },
}))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, ...rest }: { children?: React.ReactNode; to?: string } & Record<string, unknown>) =>
    <a href={to} {...rest}>{children}</a>,
  useNavigate: () => navigateMock,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) =>
    select({ location: { pathname: routerState.pathname } }),
}))
vi.mock('@/api/tasks', () => ({
  taskKeys: { mine: () => ['tasks', 'mine'] },
  listMyTasks: vi.fn(),
}))
vi.mock('@/api/notifications', () => ({
  getUnreadCount: vi.fn(),
  getNotifications: vi.fn().mockResolvedValue({ items: [], total_count: 0 }),
  markAsRead: vi.fn(),
  markAllRead: vi.fn(),
}))
vi.mock('@/api/savedSearches', () => ({
  listSavedSearches: vi.fn().mockResolvedValue([]),
  createSavedSearch: vi.fn(),
  deleteSavedSearch: vi.fn(),
}))
// Heavy children that have their own suites and no bearing on the header
// geometry asserted here.
vi.mock('@/components/notifications/NotificationsPanel', () => ({ NotificationsPanel: () => null }))
vi.mock('@/components/shared/LanguageSelector', () => ({
  LanguageSelector: ({ labelClassName }: { labelClassName?: string }) =>
    <div data-testid="language-stub" data-label-class={labelClassName} />,
}))

// jsdom has no layout, so every element reports no client rects. The shortcut
// handler asks whether the search input is rendered (display:none below the
// shell breakpoint has no rects), so layout is simulated: everything is
// "rendered" unless a test hides the search input.
let searchRendered = true
const rectsSpy = vi.spyOn(Element.prototype, 'getClientRects')

beforeEach(() => {
  searchRendered = true
  rectsSpy.mockImplementation(function (this: Element) {
    const hidden = this.tagName === 'INPUT' && !searchRendered
    return (hidden ? [] : [{}]) as unknown as DOMRectList
  })
  navigateMock.mockReset()
  routerState.pathname = '/workspaces'
  vi.mocked(listMyTasks).mockReset().mockResolvedValue([])
  vi.mocked(getUnreadCount).mockReset().mockResolvedValue(0)
  window.localStorage.clear()
  document.documentElement.classList.remove('dark')
  useUIStore.setState({ sidebarCollapsed: false, notificationsOpen: false })
  useAuthStore.setState({
    user: { id: 'u-1', email: 'a@x', role: 'owner', tenant_id: 't-1', display_name: 'Amal Haddad' } as never,
    tenantId: 't-1',
    isAuthenticated: true,
    isHydrating: false,
    hydrationPromise: null,
  })
})

afterEach(() => {
  rectsSpy.mockReset()
})

function renderTopbar(onOpenMobileNav: () => void = () => {}) {
  return renderWithProviders(
    <ThemeProvider defaultMode="light">
      <AppTopbar onOpenMobileNav={onOpenMobileNav} />
    </ThemeProvider>,
  )
}

const header = () => screen.getByRole('banner')
const verticalSeparators = () => Array.from(header().querySelectorAll('[data-orientation="vertical"]'))
const precedes = (a: Element, b: Element) =>
  Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)

// The rail appears at the `shell` breakpoint (801px), so the header's
// open-navigation button and breadcrumb/spacer swap must flip at the same
// width — otherwise there is a band with neither a rail nor a menu button.
describe('AppTopbar shell breakpoint', () => {
  it('shows the open-navigation button only below the shell breakpoint', () => {
    renderTopbar()
    const btn = screen.getByRole('button', { name: /open navigation/i })
    expect(btn).toHaveClass('shell:hidden')
    expect(btn).not.toHaveClass('lg:hidden')
  })

  it('shows the breadcrumbs from the shell breakpoint up and a spacer below it', () => {
    renderTopbar()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' }).parentElement as HTMLElement
    expect(crumbs).toHaveClass('hidden', 'shell:block')
    expect(crumbs).not.toHaveClass('lg:block')
    const spacer = crumbs.nextElementSibling as HTMLElement
    expect(spacer).toHaveClass('flex-1', 'shell:hidden')
    expect(spacer).not.toHaveClass('lg:hidden')
  })

  it('hides the language selector and the left divider below the shell breakpoint', () => {
    renderTopbar()
    expect(screen.getByTestId('language-stub').parentElement).toHaveClass('hidden', 'shell:block')
    expect(verticalSeparators()[0]).toHaveClass('hidden', 'shell:block')
  })
})

// From 801px the rail (240px) and the right cluster share the header; with
// the 280px search box and the language label the cluster overflowed up to
// about 930px. Both now wait for the `wide` breakpoint (1181px); below it a
// search icon button stands in for the box, and on phones it is the only
// route to /search in the shell.
describe('AppTopbar wide breakpoint', () => {
  it('shows the 280px search box only from the wide breakpoint', () => {
    renderTopbar()
    const wrapper = screen.getByRole('search').parentElement as HTMLElement
    expect(wrapper).toHaveClass('hidden', 'wide:flex')
    expect(wrapper).not.toHaveClass('shell:flex')
  })

  it('shows a 36px ghost search icon button wherever the box is hidden, named like the box', () => {
    renderTopbar()
    const btn = screen.getByRole('button', { name: 'Search documents…' })
    expect(btn).toHaveClass('h-9', 'w-9', 'wide:hidden')
    expect(btn).not.toHaveClass('hidden')
    expect(btn).not.toHaveClass('shell:hidden')
    expect(btn.querySelector('svg')).toHaveClass('lucide-search')
    expect(btn.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
  })

  it('the search icon button opens the search page', async () => {
    renderTopbar()
    await userEvent.click(screen.getByRole('button', { name: 'Search documents…' }))
    expect(navigateMock).toHaveBeenCalledTimes(1)
    expect(navigateMock.mock.calls[0][0]).toMatchObject({ to: '/search' })
  })

  it('hides the language label below the wide breakpoint (icon and chevron stay)', () => {
    renderTopbar()
    expect(screen.getByTestId('language-stub')).toHaveAttribute('data-label-class', 'hidden wide:inline')
  })
})

describe('AppTopbar frame', () => {
  it('is a 56px bar with a hairline bottom edge on the page background and no neumorphic shadow', () => {
    const { container } = renderTopbar()
    expect(header()).toHaveClass('h-14', 'border-b', 'border-border', 'bg-background')
    expect(container.innerHTML).not.toMatch(/shadow-neu/)
  })

  it('pads to match the content column (16px, 24px from the shell breakpoint)', () => {
    renderTopbar()
    expect(header()).toHaveClass('px-4', 'shell:px-6')
    expect(header().className).not.toMatch(/\blg:px-/)
  })

  it('orders the left cluster toggle, divider, breadcrumbs and the right cluster search to avatar', () => {
    renderTopbar()
    const toggle = screen.getByRole('button', { name: 'Collapse sidebar' })
    const [leftDivider, rightDivider] = verticalSeparators()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    const searchButton = screen.getByRole('button', { name: 'Search documents…' })
    const search = screen.getByRole('search')
    const tasks = screen.getByTestId('my-tasks-badge')
    const bell = screen.getByTestId('notifications-button')
    const language = screen.getByTestId('language-stub')
    const theme = screen.getByRole('button', { name: 'Toggle theme' })
    const avatar = screen.getByTestId('user-avatar-button')

    const order = [toggle, leftDivider, crumbs, searchButton, search, tasks, bell, language, theme, rightDivider, avatar]
    for (let i = 0; i < order.length - 1; i++) {
      expect(precedes(order[i], order[i + 1]), `element ${i} should precede element ${i + 1}`).toBe(true)
    }
    expect(verticalSeparators()).toHaveLength(2)
    expect(leftDivider).toHaveClass('h-4')
    expect(rightDivider).toHaveClass('h-4')
    expect(rightDivider).not.toHaveClass('hidden')
  })
})

describe('AppTopbar sidebar toggles', () => {
  it('collapses and expands the rail from a 32px ghost panel-left button shown from the shell breakpoint', async () => {
    renderTopbar()
    const toggle = screen.getByRole('button', { name: 'Collapse sidebar' })
    expect(toggle).toHaveClass('h-8', 'w-8', 'hidden', 'shell:inline-flex')
    expect(toggle.querySelector('svg')).toHaveClass('lucide-panel-left', 'rtl:-scale-x-100')

    await userEvent.click(toggle)
    expect(useUIStore.getState().sidebarCollapsed).toBe(true)
    const expand = screen.getByRole('button', { name: 'Expand sidebar' })
    expect(screen.queryByRole('button', { name: 'Collapse sidebar' })).toBeNull()

    await userEvent.click(expand)
    expect(useUIStore.getState().sidebarCollapsed).toBe(false)
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument()
  })

  it('labels the toggle "Expand sidebar" when the persisted state is collapsed', () => {
    useUIStore.setState({ sidebarCollapsed: true })
    renderTopbar()
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeInTheDocument()
  })

  it('opens the mobile navigation from a 32px panel-left button', async () => {
    const onOpen = vi.fn()
    renderTopbar(onOpen)
    const btn = screen.getByRole('button', { name: 'Open navigation' })
    expect(btn).toHaveClass('h-8', 'w-8')
    expect(btn.querySelector('svg')).toHaveClass('lucide-panel-left', 'rtl:-scale-x-100')
    await userEvent.click(btn)
    expect(onOpen).toHaveBeenCalledTimes(1)
    expect(useUIStore.getState().sidebarCollapsed).toBe(false)
  })
})

describe('AppTopbar breadcrumbs', () => {
  it('starts with a Home link and ends with the current page', () => {
    renderTopbar()
    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' })
    const home = within(nav).getByRole('link', { name: 'Home' })
    expect(home).toHaveAttribute('href', '/')
    expect(within(nav).getByText('Workspaces')).toBeInTheDocument()
  })
})

describe('AppTopbar search', () => {
  it('is a 280x36 outline search box named "Search documents…" with the shortcut chip at the end', () => {
    renderTopbar()
    const input = screen.getByRole('combobox', { name: 'Search documents…' })
    expect(input).toHaveAttribute('placeholder', 'Search documents…')

    const form = screen.getByRole('search')
    expect(form).toHaveClass(
      'h-9',
      'w-[280px]',
      'rounded-md',
      'border',
      'border-border',
      'bg-outline-bg',
      'shadow-xs',
    )
    expect(form.firstElementChild).toHaveClass('lucide-search')

    const kbd = form.querySelector('kbd') as HTMLElement
    expect(kbd).toHaveTextContent(shortcutLabel('K'))
    expect(kbd).toHaveAttribute('aria-hidden', 'true')
    expect(form.lastElementChild).toBe(kbd)
    expect(precedes(input, kbd)).toBe(true)
  })

  it('opens the recent-searches dropdown on focus and closes it on Escape', async () => {
    recordRecentSearch('retention policy')
    renderTopbar()
    expect(screen.queryByTestId('search-dropdown')).toBeNull()

    const input = screen.getByRole('combobox', { name: 'Search documents…' })
    await userEvent.click(input)
    const dropdown = await screen.findByTestId('search-dropdown')
    expect(dropdown).toHaveTextContent('retention policy')
    expect(input).toHaveAttribute('aria-expanded', 'true')

    await userEvent.keyboard('{Escape}')
    expect(screen.queryByTestId('search-dropdown')).toBeNull()
    expect(input).not.toHaveFocus()
  })

  it('navigates to /search with the trimmed query on Enter, then resets', async () => {
    renderTopbar()
    const input = screen.getByRole('combobox', { name: 'Search documents…' })
    await userEvent.type(input, '  master agreement {Enter}')
    expect(navigateMock).toHaveBeenCalledWith({ to: '/search', search: { q: 'master agreement' } })
    expect(input).toHaveValue('')
  })

  it('does not navigate on Enter with an empty query', async () => {
    renderTopbar()
    await userEvent.type(screen.getByRole('combobox', { name: 'Search documents…' }), '   {Enter}')
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('focuses the input on Ctrl+K', async () => {
    renderTopbar()
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(screen.getByRole('combobox', { name: 'Search documents…' })).toHaveFocus()
  })

  describe('shortcuts while the search is hidden (below the wide breakpoint)', () => {
    const press = (init: KeyboardEventInit) => {
      const event = new KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init })
      document.body.dispatchEvent(event)
      return event
    }

    it('claims Ctrl+K and "/" and focuses the input when the search is rendered', () => {
      renderTopbar()
      const input = screen.getByRole('combobox', { name: 'Search documents…' })
      expect(press({ key: 'k', ctrlKey: true }).defaultPrevented).toBe(true)
      expect(input).toHaveFocus()
      input.blur()
      expect(press({ key: '/' }).defaultPrevented).toBe(true)
      expect(input).toHaveFocus()
    })

    it('leaves Ctrl+K, Cmd+K and "/" alone when the input is not rendered', () => {
      renderTopbar()
      searchRendered = false
      const input = screen.getByRole('combobox', { name: 'Search documents…' })
      expect(press({ key: 'k', ctrlKey: true }).defaultPrevented).toBe(false)
      expect(press({ key: 'K', metaKey: true }).defaultPrevented).toBe(false)
      expect(press({ key: '/' }).defaultPrevented).toBe(false)
      expect(input).not.toHaveFocus()
      expect(screen.queryByTestId('search-dropdown')).toBeNull()
    })
  })
})

describe('AppTopbar counters', () => {
  it('shows the open-task count as a count badge on a 36px ghost button', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([
      { status: 'open' },
      { status: 'in_progress' },
      { status: 'done' },
    ] as never)
    renderTopbar()
    const badge = await screen.findByTestId('my-tasks-count')
    expect(badge).toHaveTextContent('2')
    expect(badge).toHaveClass('h-[18px]', 'min-w-[18px]', 'rounded-full', 'bg-primary')
    expect(badge.tagName).toBe('SPAN')
    const btn = screen.getByRole('button', { name: 'My tasks (2 open)' })
    expect(btn).toHaveClass('h-9', 'w-9')
    expect(btn).toContainElement(badge)
  })

  it('omits the task badge when nothing is open', async () => {
    renderTopbar()
    expect(await screen.findByRole('button', { name: 'My tasks' })).toBeInTheDocument()
    expect(screen.queryByTestId('my-tasks-count')).toBeNull()
  })

  it('shows the unread count on the notifications bell as a count badge', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(120)
    renderTopbar()
    const badge = await screen.findByTestId('notifications-unread-dot')
    expect(badge).toHaveTextContent('99+')
    expect(badge).toHaveClass('h-[18px]', 'min-w-[18px]', 'rounded-full', 'bg-primary')
    expect(badge.tagName).toBe('SPAN')
    const bell = screen.getByRole('button', { name: 'Notifications (120 unread)' })
    expect(bell).toHaveClass('h-9', 'w-9')
    expect(bell).toContainElement(badge)
  })

  it('has no badge on the bell when everything is read', async () => {
    renderTopbar()
    expect(await screen.findByRole('button', { name: 'Notifications' })).toBeInTheDocument()
    expect(screen.queryByTestId('notifications-unread-dot')).toBeNull()
  })
})

// The dashboard's Unread KPI (and later the attention list) opens this
// menu from outside the header, so its open state lives in the UI store.
describe('AppTopbar notifications menu', () => {
  it('opens when the store opens it from elsewhere on the page', async () => {
    renderTopbar()
    expect(screen.queryByTestId('notif-see-all')).toBeNull()
    act(() => useUIStore.getState().openNotifications())
    expect(await screen.findByTestId('notif-see-all')).toBeInTheDocument()
  })

  it('writes its own open and close back to the store', async () => {
    renderTopbar()
    await userEvent.click(screen.getByTestId('notifications-button'))
    expect(await screen.findByTestId('notif-see-all')).toBeInTheDocument()
    expect(useUIStore.getState().notificationsOpen).toBe(true)
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByTestId('notif-see-all')).toBeNull())
    expect(useUIStore.getState().notificationsOpen).toBe(false)
  })

  // The flag outlives the header (it is store state, not menu state), so a
  // menu open at logout would otherwise reopen on the next shell mount.
  it('resets the open flag when the header unmounts', async () => {
    const { unmount } = renderTopbar()
    act(() => useUIStore.getState().openNotifications())
    expect(await screen.findByTestId('notif-see-all')).toBeInTheDocument()
    unmount()
    expect(useUIStore.getState().notificationsOpen).toBe(false)
  })
})

describe('AppTopbar theme toggle', () => {
  const html = () => document.documentElement

  it('is a single ghost button named "Toggle theme" with no menu', async () => {
    renderTopbar()
    const btn = screen.getByRole('button', { name: 'Toggle theme' })
    expect(btn).toHaveClass('h-9', 'w-9')
    expect(btn).not.toHaveAttribute('aria-haspopup')
    await userEvent.click(btn)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('cycles light, dark, system, light and keeps <html> in step', async () => {
    renderTopbar()
    const btn = screen.getByRole('button', { name: 'Toggle theme' })
    expect(html()).not.toHaveClass('dark')
    expect(btn.querySelector('svg')).toHaveClass('lucide-sun')

    await userEvent.click(btn)
    expect(html()).toHaveClass('dark')
    expect(window.localStorage.getItem('vaultdms-theme')).toBe('dark')
    expect(btn.querySelector('svg')).toHaveClass('lucide-moon')

    // jsdom's matchMedia reports no dark preference, so "system" resolves light.
    await userEvent.click(btn)
    expect(window.localStorage.getItem('vaultdms-theme')).toBe('system')
    expect(html()).not.toHaveClass('dark')
    expect(btn.querySelector('svg')).toHaveClass('lucide-sun')

    await userEvent.click(btn)
    expect(window.localStorage.getItem('vaultdms-theme')).toBe('light')
    expect(html()).not.toHaveClass('dark')
  })

  it('names the current mode in its tooltip', async () => {
    renderTopbar()
    const btn = screen.getByRole('button', { name: 'Toggle theme' })
    expect(btn).toHaveAttribute('title', 'Toggle theme: Light')
    await userEvent.click(btn)
    expect(btn).toHaveAttribute('title', 'Toggle theme: Dark')
    await userEvent.click(btn)
    expect(btn).toHaveAttribute('title', 'Toggle theme: System')
  })
})

describe('AppTopbar account menu', () => {
  it('shows the initial on a 32px round muted button', () => {
    renderTopbar()
    const avatar = screen.getByTestId('user-avatar-button')
    expect(avatar).toHaveClass('h-8', 'w-8', 'rounded-full')
    expect(avatar).toHaveTextContent('A')
    expect(avatar).toHaveAccessibleName('Account menu')
  })
})

describe('header copy', () => {
  it('ships header.home and header.search_documents in both locales', () => {
    expect(enCommon.header).toEqual({ home: 'Home', search_documents: 'Search documents…' })
    expect(arCommon.header).toEqual({ home: 'الرئيسية', search_documents: 'ابحث في المستندات…' })
  })
})
