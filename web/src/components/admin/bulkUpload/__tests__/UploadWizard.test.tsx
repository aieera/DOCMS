import { it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('@/api/workspaces', () => ({
  getWorkspaces: vi.fn(async () => [{ id: 'ws1', name: 'Legal' }]),
}))
const runImport = vi.fn(async (..._args: unknown[]) => ({
  created: 1,
  skipped: 0,
  failed: 0,
  items: [{ relPath: 'A/x.pdf', outcome: 'created' as const }],
}))
vi.mock('@/lib/bulkUpload/runner', () => ({ runImport: (...a: unknown[]) => runImport(...a) }))

import { UploadWizard } from '../UploadWizard'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => runImport.mockClear())

it('previews a dropped folder then imports into an existing workspace', async () => {
  wrap(<UploadWizard />)
  const input = screen.getByTestId('bulk-file-input') as HTMLInputElement
  const file = new File(['x'], 'x.pdf', { type: 'application/pdf' })
  Object.defineProperty(file, 'webkitRelativePath', { value: 'root/A/x.pdf' })
  await userEvent.upload(input, file)

  // preview summary appears
  expect(await screen.findByText(/1 file/i)).toBeInTheDocument()

  // default destination = existing workspace (first one selected); run it
  await userEvent.click(await screen.findByTestId('bulk-run'))
  await waitFor(() => expect(runImport).toHaveBeenCalledOnce())

  // report renders
  expect(await screen.findByText(/created/i)).toBeInTheDocument()
})
