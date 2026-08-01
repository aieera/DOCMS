import { it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

const getRecordForDocument = vi.fn()
const listCategories = vi.fn()
vi.mock('@/api/records', () => ({
  getRecordForDocument: (...a: unknown[]) => getRecordForDocument(...a),
  listCategories: (...a: unknown[]) => listCategories(...a),
  declareRecord: vi.fn(),
  setVital: vi.fn(),
  freezeRecord: vi.fn(),
  unfreezeRecord: vi.fn(),
}))

import { DeclareRecordButton } from '../DeclareRecordButton'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  getRecordForDocument.mockReset().mockResolvedValue(null)
  listCategories.mockReset()
})

it('guides the admin to set up a file plan when no record series exist', async () => {
  listCategories.mockResolvedValue([]) // empty file plan
  wrap(<DeclareRecordButton documentId="doc1" canManage />)
  await userEvent.click(await screen.findByTestId('declare-record'))
  const empty = await screen.findByTestId('declare-record-empty')
  expect(empty).toHaveTextContent(/no record series configured/i)
  expect(screen.getByRole('link', { name: /records/i })).toHaveAttribute(
    'href',
    expect.stringContaining('/admin/records-retention'),
  )
})

it('shows the series picker when series exist', async () => {
  listCategories.mockResolvedValue([
    { id: 'c1', name: 'Contracts', code: 'C-01', node_type: 'series' },
    { id: 'c2', name: 'HR', node_type: 'category' },
  ])
  wrap(<DeclareRecordButton documentId="doc1" canManage />)
  await userEvent.click(await screen.findByTestId('declare-record'))
  expect(await screen.findByTestId('declare-record-picker')).toBeInTheDocument()
  expect(screen.getByRole('option', { name: /Contracts/i })).toBeInTheDocument()
  // 'HR' is a category, not a series — must not be offered
  expect(screen.queryByRole('option', { name: /^HR$/ })).not.toBeInTheDocument()
})
