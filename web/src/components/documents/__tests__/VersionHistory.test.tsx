// VersionHistory — focused tests on the named-versions inline edit
// (Phase 9). The label PATCH endpoint is permission-guarded server-
// side (backend requires "edit" on the document); the FE shows the
// affordance to everyone and surfaces 403/etc. via readErrorMessage.
// These tests pin the API call shape + the error-surfacing contract.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { VersionHistory } from '../VersionHistory'

vi.mock('sonner', () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}))

vi.mock('@/api/client', () => ({
  readErrorMessage: (e: unknown) => {
    const d = (e as { response?: { data?: { error?: string } } })?.response?.data
    return d?.error ?? null
  },
}))

const getVersionsMock = vi.fn()
const setVersionLabelMock = vi.fn()

vi.mock('@/api/documents', () => ({
  getVersions: (id: string) => getVersionsMock(id),
  setVersionLabel: (docId: string, verId: string, label: string) =>
    setVersionLabelMock(docId, verId, label),
}))

function wrap(children: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

const versionRow = {
  id: 'ver-1',
  document_id: 'doc-1',
  version_number: 3,
  size_bytes: 2048,
  created_by: 'u-1',
  created_by_name: 'Alice',
  created_at: '2026-05-22T00:00:00Z',
}

beforeEach(async () => {
  getVersionsMock.mockReset()
  setVersionLabelMock.mockReset()
  // Reset shared toast spies so each test starts with a clean
  // call log — without this, success counts leak across tests.
  const { toast } = await import('sonner')
  ;(toast.error as ReturnType<typeof vi.fn>).mockClear()
  ;(toast.success as ReturnType<typeof vi.fn>).mockClear()
})

describe('<VersionHistory> — named versions', () => {
  it('shows the "Name this version" CTA on unlabelled rows and reveals the input on click', async () => {
    getVersionsMock.mockResolvedValue([versionRow])

    render(wrap(<VersionHistory documentId="doc-1" />))

    const cta = await screen.findByTestId(`version-name-cta-${versionRow.id}`)
    expect(cta).toHaveTextContent(/name this version/i)
    fireEvent.click(cta)

    const input = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    expect(input).toBeInTheDocument()
    expect(input.value).toBe('')
  })

  it('rendering a labelled version shows the label badge and a pencil edit button (no CTA)', async () => {
    getVersionsMock.mockResolvedValue([{ ...versionRow, label: 'Q1 final' }])

    render(wrap(<VersionHistory documentId="doc-1" />))

    const badge = await screen.findByTestId(`version-label-${versionRow.id}`)
    expect(badge).toHaveTextContent('Q1 final')
    expect(screen.queryByTestId(`version-name-cta-${versionRow.id}`)).not.toBeInTheDocument()
    expect(screen.getByTestId(`version-label-edit-${versionRow.id}`)).toBeInTheDocument()
  })

  it('saving a label calls setVersionLabel with the trimmed value', async () => {
    getVersionsMock.mockResolvedValue([versionRow])
    setVersionLabelMock.mockResolvedValue({
      id: versionRow.id, document_id: 'doc-1', version_number: 3, label: 'Q1 final',
    })

    render(wrap(<VersionHistory documentId="doc-1" />))

    fireEvent.click(await screen.findByTestId(`version-name-cta-${versionRow.id}`))
    const input = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    fireEvent.change(input, { target: { value: '   Q1 final   ' } })
    fireEvent.click(screen.getByTestId(`version-label-save-${versionRow.id}`))

    await waitFor(() => {
      expect(setVersionLabelMock).toHaveBeenCalledWith('doc-1', versionRow.id, 'Q1 final')
    })
  })

  it('SECURITY: backend 403 on label PATCH surfaces the server error via toast (never silent success)', async () => {
    const { toast } = await import('sonner')
    getVersionsMock.mockResolvedValue([versionRow])
    setVersionLabelMock.mockRejectedValue({
      response: { status: 403, data: { error: 'forbidden: requires edit on document' } },
    })

    render(wrap(<VersionHistory documentId="doc-1" />))

    fireEvent.click(await screen.findByTestId(`version-name-cta-${versionRow.id}`))
    const input = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'attempted rename' } })
    fireEvent.click(screen.getByTestId(`version-label-save-${versionRow.id}`))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith('forbidden: requires edit on document')
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('Enter in the input submits; Escape cancels back to the CTA', async () => {
    getVersionsMock.mockResolvedValue([versionRow])
    setVersionLabelMock.mockResolvedValue({
      id: versionRow.id, document_id: 'doc-1', version_number: 3, label: 'via enter',
    })

    render(wrap(<VersionHistory documentId="doc-1" />))

    // Enter submits.
    fireEvent.click(await screen.findByTestId(`version-name-cta-${versionRow.id}`))
    const input = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'via enter' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(setVersionLabelMock).toHaveBeenCalledWith('doc-1', versionRow.id, 'via enter'))

    // Re-open and Escape — should NOT fire a second mutation.
    setVersionLabelMock.mockClear()
    // After the mutation succeeds the row's editing state resets;
    // simulate by clicking the CTA again. (Cache is stale until
    // invalidation refetches, but the input still appears.)
    const inputs = screen.queryAllByTestId(`version-label-input-${versionRow.id}`)
    if (inputs.length === 0) {
      fireEvent.click(screen.getByTestId(`version-name-cta-${versionRow.id}`))
    }
    const input2 = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    fireEvent.change(input2, { target: { value: 'cancelled' } })
    fireEvent.keyDown(input2, { key: 'Escape' })
    // Escape should NOT trigger a save.
    await new Promise((r) => setTimeout(r, 20))
    expect(setVersionLabelMock).not.toHaveBeenCalled()
  })

  it('client clamps label to 200 characters before calling the server', async () => {
    const { toast } = await import('sonner')
    getVersionsMock.mockResolvedValue([versionRow])

    render(wrap(<VersionHistory documentId="doc-1" />))

    fireEvent.click(await screen.findByTestId(`version-name-cta-${versionRow.id}`))
    const input = await screen.findByTestId(`version-label-input-${versionRow.id}`) as HTMLInputElement
    // The <input maxLength={200}> in the component enforces typing
    // limit, but fireEvent.change bypasses native maxLength. We
    // simulate a 201-char payload reaching the submit handler and
    // assert the component's own length check rejects it.
    const tooLong = 'a'.repeat(201)
    fireEvent.change(input, { target: { value: tooLong } })
    fireEvent.click(screen.getByTestId(`version-label-save-${versionRow.id}`))

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/200 characters/i))
    })
    expect(setVersionLabelMock).not.toHaveBeenCalled()
  })
})
