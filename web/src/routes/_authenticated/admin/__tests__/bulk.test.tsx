import { it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('@/components/admin/bulkUpload/UploadWizard', () => ({ UploadWizard: () => <div>WIZARD</div> }))
// The NDJSON panels pull in api modules; stub the whole bulk api to keep the render light.
vi.mock('@/api/bulk', () => ({ streamImport: vi.fn(), streamExport: vi.fn() }))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { BulkPage } from '../bulk'

it('defaults to the Upload files & folders tab and exposes an Advanced tab', async () => {
  render(<BulkPage />)
  expect(screen.getByText('WIZARD')).toBeInTheDocument()
  const advanced = screen.getByRole('tab', { name: /advanced/i })
  expect(advanced).toBeInTheDocument()

  await userEvent.click(advanced)
  expect(screen.queryByText('WIZARD')).not.toBeInTheDocument()
})
