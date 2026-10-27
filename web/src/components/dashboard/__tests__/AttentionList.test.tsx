import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import type { TFunction } from 'i18next'

import { AttentionList, taskDetail, taskRank } from '../AttentionList'
import { renderWithProviders } from '@/test/renderWithProviders'
import { listMyTasks } from '@/api/tasks'
import type { Task } from '@/api/tasks'
import { getNotifications, getUnreadCount, markAsRead } from '@/api/notifications'
import { listPendingTagSuggestions } from '@/api/intelligence'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import type { Notification, User } from '@/types/api'
import enCommon from '../../../../public/locales/en/common.json'
import arCommon from '../../../../public/locales/ar/common.json'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, search, ...rest }:
    { children: React.ReactNode; to?: string; search?: Record<string, string> } & Record<string, unknown>) =>
    <a href={search ? `${to}?${new URLSearchParams(search).toString()}` : to} {...rest}>{children}</a>,
  useNavigate: () => vi.fn(),
}))

vi.mock('@/api/tasks', () => ({
  taskKeys: { mine: () => ['tasks', 'mine'] },
  listMyTasks: vi.fn(),
}))
vi.mock('@/api/notifications', () => ({
  getUnreadCount: vi.fn(),
  getNotifications: vi.fn(),
  markAsRead: vi.fn(),
}))
vi.mock('@/api/intelligence', () => ({ listPendingTagSuggestions: vi.fn() }))

const DAY = 86_400_000
const mkTask = (o: Partial<Task> & Pick<Task, 'id' | 'title'>): Task => ({
  description: '', status: 'open', priority: 'normal', source: 'user', due_at: null,
  created_by: 'u1', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
  assignees: [], documents: [], ...o,
})
const mkNote = (o: Partial<Notification> & Pick<Notification, 'id'>): Notification => ({
  type: 'document.shared', title: 'Shared', body: 'A document was shared', read: false,
  created_at: new Date(Date.now() - 3_600_000).toISOString(), ...o,
})
const inbox = (items: Notification[]) => ({ items, total_count: items.length })
const suggestions = (total: number) => ({ suggestions: [], total, limit: 1, offset: 0 })
const asRole = (role: string) =>
  useAuthStore.setState({ user: { id: 'u1', email: 'x@y.z', display_name: 'X', role } as unknown as User })

const rows = () => within(screen.getByRole('region', { name: 'Needs your attention' })).getAllByRole('listitem')
const bodyClass = () =>
  (screen.getByRole('region', { name: 'Needs your attention' }).lastElementChild as HTMLElement).className

beforeEach(() => {
  vi.mocked(listMyTasks).mockReset().mockResolvedValue([])
  vi.mocked(getNotifications).mockReset().mockResolvedValue(inbox([]))
  vi.mocked(getUnreadCount).mockReset().mockResolvedValue(0)
  vi.mocked(markAsRead).mockReset()
  vi.mocked(listPendingTagSuggestions).mockReset().mockResolvedValue(suggestions(0))
  asRole('admin')
})

afterEach(async () => {
  useUIStore.setState({ notificationsOpen: false })
  useAuthStore.setState({ user: null })
  await act(async () => { await i18n.changeLanguage('en') })
})

describe('AttentionList', () => {
  it('lists pending suggestions first with a Review link to the tagging page\'s Review tab', async () => {
    vi.mocked(listPendingTagSuggestions).mockResolvedValue(suggestions(7))
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Overdue task', due_at: new Date(Date.now() - DAY).toISOString() })])
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
    vi.mocked(getUnreadCount).mockResolvedValue(1)
    renderWithProviders(<AttentionList />)

    await screen.findByText('Overdue task')
    const first = rows()[0]
    expect(within(first).getByText('7')).toBeInTheDocument()
    expect(within(first).getByText(/AI tag suggestions waiting/)).toBeInTheDocument()
    const review = within(first).getByRole('link', { name: 'Review' })
    expect(review).toHaveAttribute('href', '/admin/tagging?tab=review')
    expect(listPendingTagSuggestions).toHaveBeenCalledWith({ limit: 1 })
  })

  it('never asks a non-admin for suggestions, and shows no suggestions row', async () => {
    asRole('member')
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A task' })])
    renderWithProviders(<AttentionList />)
    await screen.findByText('A task')
    expect(listPendingTagSuggestions).not.toHaveBeenCalled()
    expect(screen.queryByRole('link', { name: 'Review' })).not.toBeInTheDocument()
  })

  it('offers Open for unread notifications and calls openNotifications', async () => {
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-1' }),
      mkNote({ id: 'n-2', type: 'comment.mention' }),
      mkNote({ id: 'n-3', read: true }),
    ]))
    vi.mocked(getUnreadCount).mockResolvedValue(2)
    renderWithProviders(<AttentionList />)

    expect(await screen.findByText('2 unread notifications')).toBeInTheDocument()
    expect(useUIStore.getState().notificationsOpen).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Open' }))
    expect(useUIStore.getState().notificationsOpen).toBe(true)
  })

  it('says "1 unread notification" in the singular and shows no row when none are unread', async () => {
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' }), mkNote({ id: 'n-2', read: true })]))
    vi.mocked(getUnreadCount).mockResolvedValue(1)
    const { unmount } = renderWithProviders(<AttentionList />)
    expect(await screen.findByText('1 unread notification')).toBeInTheDocument()
    unmount()

    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-2', read: true })]))
    vi.mocked(getUnreadCount).mockResolvedValue(0)
    renderWithProviders(<AttentionList />)
    await screen.findByText('Nothing needs you right now')
    expect(screen.queryByRole('button', { name: 'Open' })).not.toBeInTheDocument()
  })

  // I8: the number is the one the bell badge and the Unread KPI show (the
  // unread-count endpoint), not a count of the inbox's first page.
  it('takes the unread number from the unread count, not from the first inbox page', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(7)
    vi.mocked(getNotifications).mockResolvedValue(inbox([
      mkNote({ id: 'n-1', type: 'comment.mention', created_at: new Date(Date.now() - 60_000).toISOString() }),
      mkNote({ id: 'n-2', created_at: new Date(Date.now() - 7_200_000).toISOString() }),
      mkNote({ id: 'n-3', read: true }),
    ]))
    renderWithProviders(<AttentionList />)
    const title = await screen.findByText('7 unread notifications')
    const row = title.closest('li') as HTMLElement
    // The inbox page still names the newest unread item in the sub-line.
    expect(row).toHaveTextContent('Mentioned in a comment')
    expect(screen.queryByText('2 unread notifications')).not.toBeInTheDocument()
  })

  it('shows the row from the count alone when the first inbox page has no unread item', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(4)
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1', read: true })]))
    renderWithProviders(<AttentionList />)
    const title = await screen.findByText('4 unread notifications')
    const row = title.closest('li') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Open' })).toBeInTheDocument()
    // No sub-line: there is no unread item on hand to describe.
    expect(title.nextElementSibling).toBeNull()
  })

  it('shows no unread row when the count is 0, whatever the inbox page holds', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(0)
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
    renderWithProviders(<AttentionList />)
    await screen.findByText('Nothing needs you right now')
    expect(screen.queryByRole('button', { name: 'Open' })).not.toBeInTheDocument()
  })

  it('accepts the count as an int64 string, as the gateway may send it', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue('12' as unknown as number)
    renderWithProviders(<AttentionList />)
    expect(await screen.findByText('12 unread notifications')).toBeInTheDocument()
  })

  // M9 (assertions dropped with NeedsAttention, restored for AttentionList).
  it('never shows a raw notification type code in the unread sub-line', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(1)
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1', type: 'comment.mention' })]))
    renderWithProviders(<AttentionList />)
    const row = (await screen.findByText('1 unread notification')).closest('li') as HTMLElement
    expect(row).toHaveTextContent('Mentioned in a comment')
    expect(row.textContent).not.toContain('comment.mention')
    expect(row.textContent).not.toMatch(/\b[a-z]+\.[a-z_]+\b/)
  })

  it('changes nothing: rendering and opening the notifications mark nothing read', async () => {
    vi.mocked(getUnreadCount).mockResolvedValue(2)
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' }), mkNote({ id: 'n-2' })]))
    renderWithProviders(<AttentionList />)
    await userEvent.click(await screen.findByRole('button', { name: 'Open' }))
    expect(markAsRead).not.toHaveBeenCalled()
  })

  // I6: a title wraps to a second line instead of being cut off.
  it('lets a row title wrap to two lines; the sub-line stays on one', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A long task title', priority: 'high' })])
    renderWithProviders(<AttentionList />)
    const title = await screen.findByText('A long task title')
    expect(title).toHaveClass('line-clamp-2', 'text-sm', 'font-medium')
    expect(title).not.toHaveClass('truncate')
    const detail = title.nextElementSibling as HTMLElement
    expect(detail).toHaveTextContent('High · No due date')
    expect(detail).toHaveClass('truncate')
  })

  it('ranks an overdue task above an urgent one', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([
      mkTask({ id: 't-urgent', title: 'Urgent task', priority: 'urgent' }),
      mkTask({ id: 't-overdue', title: 'Overdue task', due_at: new Date(Date.now() - DAY).toISOString() }),
    ])
    renderWithProviders(<AttentionList />)
    await screen.findByText('Overdue task')
    const texts = rows().map((li) => li.textContent ?? '')
    expect(texts.findIndex((x) => x.includes('Overdue task'))).toBeLessThan(texts.findIndex((x) => x.includes('Urgent task')))
    expect(texts[0]).toContain('Overdue')
  })

  it('keeps only the top 3 open tasks, each with a View link to /tasks, after suggestions and unread', async () => {
    vi.mocked(listPendingTagSuggestions).mockResolvedValue(suggestions(2))
    vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
    vi.mocked(getUnreadCount).mockResolvedValue(1)
    vi.mocked(listMyTasks).mockResolvedValue([
      mkTask({ id: 't-other', title: 'Other task' }),
      mkTask({ id: 't-wf', title: 'Workflow task', source: 'workflow' }),
      mkTask({ id: 't-high', title: 'High task', priority: 'high' }),
      mkTask({ id: 't-urgent', title: 'Urgent task', priority: 'urgent' }),
      mkTask({ id: 't-done', title: 'Done task', status: 'done', priority: 'urgent' }),
      mkTask({ id: 't-overdue', title: 'Overdue task', due_at: new Date(Date.now() - DAY).toISOString() }),
    ])
    renderWithProviders(<AttentionList />)
    await screen.findByText('Overdue task')

    const items = rows()
    expect(items).toHaveLength(5)
    expect(items[0].textContent).toContain('AI tag suggestions waiting')
    expect(items[1].textContent).toContain('1 unread notification')
    expect(items.slice(2).map((li) => li.textContent)).toEqual([
      expect.stringContaining('Overdue task'),
      expect.stringContaining('Urgent task'),
      expect.stringContaining('High task'),
    ])
    for (const li of items.slice(2)) {
      expect(within(li).getByRole('link', { name: 'View' })).toHaveAttribute('href', '/tasks')
    }
    for (const gone of ['Workflow task', 'Other task', 'Done task']) {
      expect(screen.queryByText(gone)).not.toBeInTheDocument()
    }
  })

  it('renders the row action as a small outline button and the icon in a 32px muted tile', async () => {
    vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A task' })])
    renderWithProviders(<AttentionList />)
    await screen.findByText('A task')
    const li = rows()[0]
    const view = within(li).getByRole('link', { name: 'View' })
    expect(view).toHaveClass('border', 'h-8')
    const tile = li.querySelector('svg')!.parentElement as HTMLElement
    expect(tile).toHaveClass('h-8', 'w-8', 'rounded-md', 'bg-muted')
    expect(li.outerHTML).not.toMatch(/shadow-neu/)
  })

  it('renders the empty row when there is nothing', async () => {
    renderWithProviders(<AttentionList />)
    const text = await screen.findByText('Nothing needs you right now')
    const li = text.closest('li') as HTMLElement
    expect(li).not.toBeNull()
    expect(li.querySelector('svg')).not.toBeNull()
    expect(within(li).queryByRole('button')).not.toBeInTheDocument()
    expect(within(li).queryByRole('link')).not.toBeInTheDocument()
  })

  it('titles the card and links "View all" to /tasks', async () => {
    renderWithProviders(<AttentionList />)
    expect(await screen.findByRole('heading', { level: 2, name: 'Needs your attention' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'View all' })).toHaveAttribute('href', '/tasks')
  })

  it('reuses the shared query keys: tasks, notifications-inbox, the unread count and pending-tag-suggestions-count', async () => {
    vi.mocked(listPendingTagSuggestions).mockResolvedValue(suggestions(3))
    const { client } = renderWithProviders(<AttentionList />)
    await screen.findByText('3')
    expect(client.getQueryData(['tasks', 'mine'])).toEqual([])
    expect(client.getQueryData(['notifications-inbox'])).toEqual(inbox([]))
    expect(client.getQueryData(['notifications', 'unread-count'])).toBe(0)
    expect(client.getQueryData(['pending-tag-suggestions-count'])).toEqual(suggestions(3))
    expect(getNotifications).toHaveBeenCalledWith()
  })

  describe('states', () => {
    it('renders no rows and a busy region while a source is still pending', async () => {
      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A task' })])
      vi.mocked(getNotifications).mockReturnValue(new Promise(() => {}))
      const { container } = renderWithProviders(<AttentionList />)
      await waitFor(() => expect(listMyTasks).toHaveBeenCalled())
      await new Promise((r) => setTimeout(r, 20))
      expect(container.querySelectorAll('li')).toHaveLength(0)
      expect(screen.getByRole('region', { name: 'Needs your attention' })).toHaveAttribute('aria-busy', 'true')
    })

    it('tasks failed: an inline notice with its own Retry, the other rows stay, never the empty copy', async () => {
      vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
      vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
      vi.mocked(getUnreadCount).mockResolvedValue(1)
      renderWithProviders(<AttentionList />)
      expect(await screen.findByText(/couldn.t load your tasks/i)).toBeInTheDocument()
      expect(screen.getByText('1 unread notification')).toBeInTheDocument()
      expect(screen.queryByText('Nothing needs you right now')).not.toBeInTheDocument()

      vi.mocked(listMyTasks).mockClear().mockResolvedValue([])
      vi.mocked(getNotifications).mockClear()
      await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
      await waitFor(() => expect(listMyTasks).toHaveBeenCalledTimes(1))
      expect(getNotifications).not.toHaveBeenCalled()
    })

    it('notifications failed: an inline notice, the task rows stay, never the empty copy', async () => {
      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Real task' })])
      vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
      renderWithProviders(<AttentionList />)
      expect(await screen.findByText(/couldn.t load notifications/i)).toBeInTheDocument()
      expect(screen.getByText('Real task')).toBeInTheDocument()
      expect(screen.queryByText('Nothing needs you right now')).not.toBeInTheDocument()
    })

    it('notifications failed: its Retry refetches the notifications, not the tasks', async () => {
      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Real task' })])
      vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
      renderWithProviders(<AttentionList />)
      await screen.findByText(/couldn.t load notifications/i)

      vi.mocked(listMyTasks).mockClear()
      vi.mocked(getNotifications).mockClear().mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
      vi.mocked(getUnreadCount).mockClear().mockResolvedValue(1)
      await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
      await waitFor(() => expect(getNotifications).toHaveBeenCalledTimes(1))
      expect(await screen.findByText('1 unread notification')).toBeInTheDocument()
      expect(screen.queryByText(/couldn.t load notifications/i)).not.toBeInTheDocument()
      expect(listMyTasks).not.toHaveBeenCalled()
    })

    it('a failed unread count is a notifications failure too, never a silent missing row', async () => {
      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'Real task' })])
      vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' })]))
      vi.mocked(getUnreadCount).mockRejectedValue(new Error('boom'))
      renderWithProviders(<AttentionList />)
      expect(await screen.findByText(/couldn.t load notifications/i)).toBeInTheDocument()
      expect(screen.queryByText(/unread notification/)).not.toBeInTheDocument()
      expect(screen.getByText('Real task')).toBeInTheDocument()
    })

    it('both failed: one whole-card failure whose Retry refetches both', async () => {
      vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
      vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
      renderWithProviders(<AttentionList />)
      const retry = await screen.findByRole('button', { name: /retry/i })
      expect(screen.getAllByRole('button', { name: /retry/i })).toHaveLength(1)
      vi.mocked(listMyTasks).mockClear().mockResolvedValue([])
      vi.mocked(getNotifications).mockClear().mockResolvedValue(inbox([]))
      await userEvent.click(retry)
      await waitFor(() => expect(listMyTasks).toHaveBeenCalledTimes(1))
      expect(getNotifications).toHaveBeenCalledTimes(1)
    })

    it('a failed suggestions probe (403 for a demoted admin) leaves no row and no notice', async () => {
      vi.mocked(listPendingTagSuggestions).mockRejectedValue(new Error('forbidden'))
      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A task' })])
      renderWithProviders(<AttentionList />)
      await screen.findByText('A task')
      expect(rows()).toHaveLength(1)
      expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
    })

    it('reserves a min-height only while loading; rows and the empty state size to their content', async () => {
      let resolve: (t: Task[]) => void = () => {}
      vi.mocked(listMyTasks).mockReturnValue(new Promise<Task[]>((r) => { resolve = r }))
      const { unmount } = renderWithProviders(<AttentionList />)
      // The skeleton reserves five rows (the min-height includes the 20px body padding).
      expect(bodyClass()).toMatch(/\bmin-h-\[\d+px\]/)
      resolve([])
      await screen.findByText('Nothing needs you right now')
      expect(bodyClass()).not.toMatch(/\bmin-h-/)
      unmount()

      vi.mocked(listMyTasks).mockResolvedValue([mkTask({ id: 't-1', title: 'A task' })])
      renderWithProviders(<AttentionList />)
      await screen.findByText('A task')
      expect(bodyClass()).not.toMatch(/\bmin-h-/)
    })

    it('reserves nothing when both halves failed, even while the suggestions probe is still pending', async () => {
      vi.mocked(listMyTasks).mockRejectedValue(new Error('boom'))
      vi.mocked(getNotifications).mockRejectedValue(new Error('boom'))
      vi.mocked(listPendingTagSuggestions).mockReturnValue(new Promise(() => {}))
      renderWithProviders(<AttentionList />)
      await screen.findByRole('button', { name: /retry/i })
      expect(bodyClass()).not.toMatch(/\bmin-h-/)
    })
  })

  describe('in Arabic', () => {
    const withArabic = async () => {
      i18n.addResourceBundle('ar', 'common', arCommon, true, true)
      await act(async () => { await i18n.changeLanguage('ar') })
    }

    it('translates the card chrome, the row actions and the task details', async () => {
      await withArabic()
      vi.mocked(listPendingTagSuggestions).mockResolvedValue(suggestions(7))
      vi.mocked(getNotifications).mockResolvedValue(inbox([mkNote({ id: 'n-1' }), mkNote({ id: 'n-2' })]))
      vi.mocked(getUnreadCount).mockResolvedValue(2)
      vi.mocked(listMyTasks).mockResolvedValue([
        mkTask({ id: 't-1', title: 'late', priority: 'urgent', due_at: new Date(Date.now() - DAY).toISOString() }),
        mkTask({ id: 't-2', title: 'wf', source: 'workflow' }),
        mkTask({ id: 't-3', title: 'none', priority: 'high' }),
      ])
      renderWithProviders(<AttentionList />)
      await screen.findByText('late')

      expect(screen.getByRole('heading', { level: 2, name: 'يحتاج إلى انتباهك' })).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'مراجعة' })).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'فتح' })).toBeInTheDocument()
      expect(screen.getAllByRole('link', { name: 'عرض' })).toHaveLength(3)
      expect(screen.getByText('2 إشعاران غير مقروءين')).toBeInTheDocument()

      const text = document.body.textContent ?? ''
      expect(text).toContain('عاجلة · متأخرة')
      expect(text).toContain('خطوة موافقة')
      expect(text).toContain('عالية · بلا موعد استحقاق')
      expect(text).not.toMatch(/Overdue|Approval step|No due date|Urgent|\bHigh\b/)
    })
  })
})

describe('taskRank and taskDetail', () => {
  const t = i18n.getFixedT('en', 'common') as unknown as TFunction<'common'>
  const now = Date.now()

  it('ranks overdue 0, urgent 2, high 3, workflow 4 and anything else 5', () => {
    expect(taskRank(mkTask({ id: 'a', title: 'a', due_at: new Date(now - DAY).toISOString() }), now)).toBe(0)
    expect(taskRank(mkTask({ id: 'a', title: 'a', priority: 'urgent' }), now)).toBe(2)
    expect(taskRank(mkTask({ id: 'a', title: 'a', priority: 'high' }), now)).toBe(3)
    expect(taskRank(mkTask({ id: 'a', title: 'a', source: 'workflow' }), now)).toBe(4)
    expect(taskRank(mkTask({ id: 'a', title: 'a' }), now)).toBe(5)
  })

  it('builds the sub-line from catalogue keys, joined with a middle dot', () => {
    const past = new Date(now - DAY).toISOString()
    const future = new Date(now + DAY).toISOString()
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a', due_at: past }), true)).toMatch(/^Overdue /)
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a', due_at: future }), false)).toMatch(/^Due /)
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a', source: 'workflow' }), false)).toBe('Approval step')
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a' }), false)).toBe('No due date')
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a', priority: 'urgent' }), false)).toBe('Urgent · No due date')
    expect(taskDetail(t, mkTask({ id: 'a', title: 'a', priority: 'high' }), false)).toBe('High · No due date')
  })
})

describe('catalogue', () => {
  const widgets = (c: typeof enCommon) => c.dashboard.widgets as Record<string, string>
  const NEW_KEYS = [
    'review', 'open', 'view', 'attention_notifications_error',
    'detail_overdue', 'detail_due', 'detail_approval_step', 'detail_no_due',
    'priority_urgent', 'priority_high',
  ]

  it('ships every new key in en and ar', () => {
    for (const k of NEW_KEYS) {
      expect(widgets(enCommon)[k], `en ${k}`).toBeTruthy()
      expect(widgets(arCommon)[k], `ar ${k}`).toBeTruthy()
    }
  })

  it('keeps the {{when}} placeholder in the overdue and due strings', () => {
    for (const c of [enCommon, arCommon]) {
      expect(widgets(c).detail_overdue).toContain('{{when}}')
      expect(widgets(c).detail_due).toContain('{{when}}')
    }
  })

  it('ships unread_rows with one/other in en and all six CLDR forms in ar', () => {
    for (const f of ['one', 'other']) expect(widgets(enCommon)[`unread_rows_${f}`], `en ${f}`).toContain('{{count}}')
    for (const f of ['zero', 'one', 'two', 'few', 'many', 'other']) {
      expect(widgets(arCommon)[`unread_rows_${f}`], `ar ${f}`).toBeTruthy()
    }
  })

  it('resolves the Arabic plural form for each count class', () => {
    const ar = i18n.getFixedT('ar', 'common')
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    const out = [0, 1, 2, 3, 11, 100].map((count) => ar('dashboard.widgets.unread_rows', { count }))
    expect(new Set(out).size).toBeGreaterThanOrEqual(4)
    expect(out[2]).toBe('2 إشعاران غير مقروءين')
    expect(out[3]).toBe('3 إشعارات غير مقروءة')
    for (const s of out) expect(s).not.toContain('unread_rows')
  })
})
