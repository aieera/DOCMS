import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { AppSidebar, MobileSidebar, SidebarContent } from '../app-sidebar'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { listSmartFolders } from '@/api/savedSearches'

// The smart-folders block is the only network reader inside the sidebar.
vi.mock('@/api/savedSearches', () => ({ listSmartFolders: vi.fn() }))

function setRole(role: string, extra: Record<string, unknown> = {}) {
  useAuthStore.setState({
    user: { id: 'u-1', email: 'a@x', role, tenant_id: 't-1', ...extra } as never,
    tenantId: 't-1',
    isAuthenticated: true,
    isHydrating: false,
    hydrationPromise: null,
  })
}

// A real TanStack memory router: the sidebar reads the pathname from
// useRouterState and renders real <Link>s, so the active state is
// exercised through the router rather than a mocked hook. Index + splat
// routes exist only so every location resolves and the root component
// (the thing under test) renders instead of a not-found screen.
async function renderAt(path: string, ui: ReactNode) {
  const rootRoute = createRootRoute({ component: () => <>{ui}</> })
  const routeTree = rootRoute.addChildren([
    createRoute({ getParentRoute: () => rootRoute, path: '/' }),
    createRoute({ getParentRoute: () => rootRoute, path: '$' }),
  ])
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  let utils!: ReturnType<typeof render>
  await act(async () => {
    utils = render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    )
  })
  return utils
}

const noop = () => {}

beforeEach(() => {
  // The router's scroll restoration calls window.scrollTo, which jsdom
  // reports as "not implemented" on every render.
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.mocked(listSmartFolders).mockReset().mockResolvedValue([])
  setRole('owner')
  useUIStore.setState({ sidebarCollapsed: false })
})

describe('SidebarContent', () => {
  it('marks the current route as the active page and no other', async () => {
    await renderAt('/tasks', <SidebarContent collapsed={false} onToggle={noop} />)
    const tasks = screen.getByRole('link', { name: 'Tasks' })
    expect(tasks).toHaveAttribute('aria-current', 'page')
    expect(tasks).toHaveClass('bg-sidebar-accent', 'text-sidebar-accent-foreground')
    const trash = screen.getByRole('link', { name: 'Trash' })
    expect(trash).not.toHaveAttribute('aria-current')
    expect(trash).not.toHaveClass('bg-sidebar-accent')
    // Exactly one active link in the whole rail.
    expect(document.querySelectorAll('[aria-current="page"]')).toHaveLength(1)
  })

  it('treats a nested path as active for its section', async () => {
    await renderAt('/admin/users', <SidebarContent collapsed={false} onToggle={noop} />)
    expect(screen.getByRole('link', { name: 'Admin' })).toHaveAttribute('aria-current', 'page')
  })

  it('renders items as 32px rows with the 8px radius, 14px text and 8px gap', async () => {
    await renderAt('/tasks', <SidebarContent collapsed={false} onToggle={noop} />)
    const link = screen.getByRole('link', { name: 'Trash' })
    expect(link).toHaveClass('h-8', 'rounded-md', 'px-2', 'text-sm', 'gap-2')
    // 16px icon.
    expect(link.querySelector('svg')).toHaveClass('h-4', 'w-4')
  })

  it('shows the brand mark tile, name and the tagline', async () => {
    await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
    expect(screen.getByText('Document management')).toBeInTheDocument()
    const name = screen.getByText('SeDoc')
    const tile = name.closest('a')!.querySelector('.bg-primary')!
    expect(tile).toHaveClass('h-8', 'w-8', 'rounded-md', 'bg-primary')
    expect(tile.querySelector('svg')).toBeInTheDocument()
  })

  it('gives the brand row a 56px height', async () => {
    await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
    const row = screen.getByText('SeDoc').closest('div')!
    expect(row).toHaveClass('h-14')
  })

  it('lists the four groups with 32px muted 12px-medium labels', async () => {
    await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
    for (const name of ['Workspace', 'Integrations', 'Inbox', 'Settings']) {
      const group = screen.getByRole('group', { name })
      const label = within(group).getByText(name, { selector: 'div' })
      expect(label).toHaveClass('h-8', 'text-xs', 'font-medium', 'text-muted-foreground')
      expect(label).not.toHaveClass('uppercase')
    }
  })

  it('pins the Settings group to the bottom of the rail', async () => {
    await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
    expect(screen.getByRole('group', { name: 'Settings' })).toHaveClass('mt-auto')
    expect(screen.getByRole('group', { name: 'Workspace' })).not.toHaveClass('mt-auto')
    expect(screen.getByRole('group', { name: 'Inbox' })).not.toHaveClass('mt-auto')
    // Settings is the last group.
    const groups = screen.getAllByRole('group')
    expect(groups[groups.length - 1]).toBe(screen.getByRole('group', { name: 'Settings' }))
  })

  it('does not use the neumorphic shadows', async () => {
    const { container } = await renderAt('/tasks', <SidebarContent collapsed={false} onToggle={noop} />)
    expect(container.innerHTML).not.toMatch(/shadow-neu/)
  })

  describe('role gating', () => {
    it('shows Admin, Reports and Integrations to an owner', async () => {
      await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
      expect(screen.getByRole('link', { name: 'Admin' })).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Reports' })).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Integrations' })).toBeInTheDocument()
    })

    it('hides every gated link and the Integrations group from a member', async () => {
      setRole('member')
      await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
      expect(screen.queryByRole('link', { name: 'Admin' })).toBeNull()
      expect(screen.queryByRole('link', { name: 'Reports' })).toBeNull()
      expect(screen.queryByRole('link', { name: 'Integrations' })).toBeNull()
      expect(screen.queryByRole('group', { name: 'Integrations' })).toBeNull()
      expect(screen.getByRole('link', { name: 'My settings' })).toBeInTheDocument()
    })

    it('shows Admin but not Reports to a compliance officer', async () => {
      setRole('compliance_officer')
      await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
      expect(screen.getByRole('link', { name: 'Admin' })).toBeInTheDocument()
      expect(screen.queryByRole('link', { name: 'Reports' })).toBeNull()
    })

    it('shows everything to a platform admin regardless of role', async () => {
      setRole('member', { is_platform_admin: true })
      await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
      expect(screen.getByRole('link', { name: 'Admin' })).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Reports' })).toBeInTheDocument()
    })
  })

  describe('collapsed', () => {
    it('drops the labels, tagline and group headings but keeps accessible names and titles', async () => {
      await renderAt('/tasks', <SidebarContent collapsed onToggle={noop} />)
      expect(screen.queryByText('Tasks')).toBeNull()
      expect(screen.queryByText('Document management')).toBeNull()
      expect(screen.queryByText('SeDoc')).toBeNull()
      const tasks = screen.getByRole('link', { name: 'Tasks' })
      expect(tasks).toHaveAttribute('title', 'Tasks')
      expect(tasks).toHaveAttribute('aria-current', 'page')
      expect(screen.getByRole('link', { name: 'Admin' })).toHaveAttribute('title', 'Admin')
    })

    it('keeps the Settings group pinned to the bottom', async () => {
      await renderAt('/', <SidebarContent collapsed onToggle={noop} />)
      expect(screen.getByRole('group', { name: 'Settings' })).toHaveClass('mt-auto')
    })

    it('offers an expand button that calls onToggle', async () => {
      const onToggle = vi.fn()
      await renderAt('/', <SidebarContent collapsed onToggle={onToggle} />)
      await userEvent.click(screen.getByRole('button', { name: /expand/i }))
      expect(onToggle).toHaveBeenCalledTimes(1)
    })
  })

  it('offers a collapse button in the brand row when expanded', async () => {
    const onToggle = vi.fn()
    await renderAt('/', <SidebarContent collapsed={false} onToggle={onToggle} />)
    const btn = screen.getByRole('button', { name: /collapse/i })
    expect(btn).toHaveClass('h-8', 'w-8')
    await userEvent.click(btn)
    expect(onToggle).toHaveBeenCalledTimes(1)
  })

  describe('smart folders', () => {
    it('renders saved smart folders inside the Workspace group when expanded', async () => {
      vi.mocked(listSmartFolders).mockResolvedValue([
        { id: 'sf-1', name: 'Expiring contracts', query: 'contract', tree_visibility: 'private' } as never,
      ])
      await renderAt('/', <SidebarContent collapsed={false} onToggle={noop} />)
      const workspace = screen.getByRole('group', { name: 'Workspace' })
      const folder = await within(workspace).findByTestId('smart-folder-sf-1')
      expect(folder).toHaveTextContent('Expiring contracts')
      expect(folder).toHaveClass('h-8', 'rounded-md')
      expect(within(workspace).getByText('Smart folders')).toBeInTheDocument()
      // ... and so above the pinned Settings group, not below it.
      const settings = screen.getByRole('group', { name: 'Settings' })
      expect(folder.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    })

    it('hides smart folders when collapsed', async () => {
      vi.mocked(listSmartFolders).mockResolvedValue([
        { id: 'sf-1', name: 'Expiring contracts', query: 'contract' } as never,
      ])
      await renderAt('/', <SidebarContent collapsed onToggle={noop} />)
      expect(screen.queryByTestId('smart-folder-sf-1')).toBeNull()
    })
  })
})

describe('AppSidebar', () => {
  it('is a 240px flat sidebar-surface rail that only shows from the shell breakpoint', async () => {
    await renderAt('/', <AppSidebar />)
    const aside = screen.getByTestId('app-sidebar')
    expect(aside.tagName).toBe('ASIDE')
    expect(aside).toHaveClass('w-60', 'bg-sidebar', 'border-e', 'border-sidebar-border')
    expect(aside).toHaveClass('hidden', 'shell:block')
    expect(aside).not.toHaveClass('lg:block')
    expect(aside.className).not.toMatch(/shadow-neu/)
  })

  it('shrinks to 56px when the store says collapsed', async () => {
    useUIStore.setState({ sidebarCollapsed: true })
    await renderAt('/', <AppSidebar />)
    const aside = screen.getByTestId('app-sidebar')
    expect(aside).toHaveClass('w-14')
    expect(aside).not.toHaveClass('w-60')
  })

  it('toggles the persisted collapsed state from the brand-row button', async () => {
    await renderAt('/', <AppSidebar />)
    await userEvent.click(screen.getByRole('button', { name: /collapse/i }))
    expect(useUIStore.getState().sidebarCollapsed).toBe(true)
    expect(screen.getByTestId('app-sidebar')).toHaveClass('w-14')
  })
})

describe('MobileSidebar', () => {
  it('opens a navigation sheet that is hidden from the shell breakpoint up', async () => {
    await renderAt('/tasks', <MobileSidebar open onOpenChange={noop} />)
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveClass('shell:hidden', 'bg-sidebar')
    expect(dialog).not.toHaveClass('lg:hidden')
    expect(within(dialog).getByRole('link', { name: 'Tasks' })).toHaveAttribute('aria-current', 'page')
    // The sheet always shows the expanded rail.
    expect(within(dialog).getByText('Document management')).toBeInTheDocument()
  })

  it('closes through the brand-row toggle', async () => {
    const onOpenChange = vi.fn()
    await renderAt('/', <MobileSidebar open onOpenChange={onOpenChange} />)
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: /collapse/i }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
