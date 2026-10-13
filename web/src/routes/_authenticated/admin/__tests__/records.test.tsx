import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
const deleteSchedule = vi.fn(async (..._a: unknown[]) => undefined)
vi.mock('@/api/records', () => ({
  listSchedules: vi.fn(async () => [
    { id: 's-1', name: 'Contracts 7y', trigger_event: 'declaration', retention_period_days: 2555, disposition_action: 'destroy' },
  ]),
  createSchedule: vi.fn(),
  deleteSchedule: (...a: unknown[]) => deleteSchedule(...a),
  listCategories: vi.fn(async () => []),
  createCategory: vi.fn(),
  updateCategory: vi.fn(),
  deleteCategory: vi.fn(),
  dispositionQueue: vi.fn(async () => []),
  disposeRecord: vi.fn(),
}))

import { useAuthStore } from '@/store/authStore'
import { RecordsAdminPage } from '@/routes/_authenticated/admin/records'

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><RecordsAdminPage /></QueryClientProvider>)
}

// W-4: deleting a retention schedule fired on a single click.
describe('records — schedule delete requires confirmation (W-4)', () => {
  beforeEach(() => {
    deleteSchedule.mockClear()
    useAuthStore.setState({
      user: { id: 'u', email: 'a@x', role: 'admin', tenant_id: 't' } as never,
      tenantId: 't', isAuthenticated: true, isHydrating: false, hydrationPromise: null,
    })
  })

  it('asks before deleting, deletes only on confirm', async () => {
    wrap()
    fireEvent.click(screen.getByTestId('records-tab-schedules'))
    await screen.findByText('Contracts 7y')
    fireEvent.click(screen.getByTestId('delete-schedule-s-1'))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent('Contracts 7y')
    expect(deleteSchedule).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Delete schedule' }))
    await waitFor(() => expect(deleteSchedule).toHaveBeenCalledWith('s-1'))
  })
})
