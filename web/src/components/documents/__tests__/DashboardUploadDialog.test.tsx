import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/renderWithProviders'
import { DashboardUploadDialog } from '@/components/documents/DashboardUploadDialog'
import { getWorkspaces, getFolders } from '@/api/workspaces'
import { useUpload } from '@/hooks/useUpload'
import type { FilingDecision } from '@/components/documents/FilingSuggestionPanel'

// Dashboard upload entry point: pick a file, pick a workspace (required),
// optionally a folder; AI filing suggestions ride the existing
// FilingSuggestionPanel and land in the FilingDecision passed to
// useUpload (which applies folder/tags AND posts the training feedback —
// the dialog must NOT double-send feedback itself).

vi.mock('@/api/workspaces', () => ({
  getWorkspaces: vi.fn(),
  getFolders: vi.fn(),
}))

const uploadFilesMock = vi.fn().mockResolvedValue(undefined)
vi.mock('@/hooks/useUpload', () => ({
  useUpload: vi.fn(() => ({ uploadFiles: uploadFilesMock })),
}))

// Stub the panel: immediately hands the parent a canned decision (or
// null). The panel's own toggle/chip behavior is covered by its module.
let panelDecision: FilingDecision | null = null
vi.mock('@/components/documents/FilingSuggestionPanel', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/components/documents/FilingSuggestionPanel')>()),
  FilingSuggestionPanel: ({ onChange }: { onChange: (d: FilingDecision | null) => void }) => {
    // Fire once on mount, mimicking the real panel's onChange push.
    void Promise.resolve().then(() => onChange(panelDecision))
    return <div data-testid="filing-panel-stub" />
  },
}))

const DECISION: FilingDecision = {
  predictionId: 'p1',
  classAccepted: true,
  folderAccepted: true,
  tagsAccepted: ['invoice'],
  tagsRejected: [],
  finalClass: 'invoice',
  finalFolderId: 'f9',
  finalTags: ['invoice'],
  predictedClass: 'invoice',
  predictedClassScore: 0.92,
  predictedFolderId: 'f9',
  predictedFolderScore: 0.92,
  predictedTags: ['invoice'],
}

function pdf(name = 'inv.pdf') {
  return new File(['%PDF-1.4'], name, { type: 'application/pdf' })
}

beforeEach(() => {
  vi.clearAllMocks()
  panelDecision = null
  vi.mocked(getWorkspaces).mockResolvedValue([
    { id: 'w1', name: 'Default Workspace' } as never,
    { id: 'w2', name: 'Finance' } as never,
  ])
  vi.mocked(getFolders).mockResolvedValue([
    { id: 'root', name: 'Root' } as never,
    { id: 'f9', name: 'Invoices', parent_folder_id: 'root' } as never,
  ])
})

async function arrange({ decision = null as FilingDecision | null } = {}) {
  panelDecision = decision
  const utils = renderWithProviders(<DashboardUploadDialog open onOpenChange={() => {}} />)
  const input = (await screen.findByTestId('dashboard-upload-input')) as HTMLInputElement
  return { ...utils, input }
}

describe('DashboardUploadDialog', () => {
  it('disables Upload until a file and workspace are chosen', async () => {
    const { input } = await arrange()
    const submit = screen.getByRole('button', { name: /^upload$/i })
    expect(submit).toHaveProperty('disabled', true)

    await userEvent.upload(input, pdf())
    expect(submit).toHaveProperty('disabled', true) // still no workspace

    await userEvent.selectOptions(await screen.findByLabelText(/workspace/i), 'w1')
    await waitFor(() => expect(submit).toHaveProperty('disabled', false))
  })

  it('uploads with the panel decision so useUpload applies folder/tags + feedback', async () => {
    const { input } = await arrange({ decision: DECISION })
    await userEvent.upload(input, pdf())
    await userEvent.selectOptions(await screen.findByLabelText(/workspace/i), 'w1')
    await userEvent.click(screen.getByRole('button', { name: /^upload$/i }))

    await waitFor(() => expect(uploadFilesMock).toHaveBeenCalledTimes(1))
    const [files, decisions] = uploadFilesMock.mock.calls[0]
    expect(files).toHaveLength(1)
    expect(decisions?.[0]).toMatchObject({ predictionId: 'p1', folderAccepted: true, finalFolderId: 'f9' })
    // Hook got the workspace; folder left to the decision/root fallback.
    // (Not "last called" — the dialog resets its state after a
    // successful submit, so the final render calls the hook bare.)
    expect(vi.mocked(useUpload)).toHaveBeenCalledWith('w1', undefined)
  })

  it('a manual folder pick overrides the suggested folder', async () => {
    const { input } = await arrange({ decision: DECISION })
    await userEvent.upload(input, pdf())
    await userEvent.selectOptions(await screen.findByLabelText(/workspace/i), 'w1')
    await userEvent.selectOptions(await screen.findByLabelText(/folder/i), 'root')
    await userEvent.click(screen.getByRole('button', { name: /^upload$/i }))

    await waitFor(() => expect(uploadFilesMock).toHaveBeenCalledTimes(1))
    const [, decisions] = uploadFilesMock.mock.calls[0]
    // folderAccepted flipped off so the hook's folderId (manual pick) wins.
    expect(decisions?.[0]).toMatchObject({ folderAccepted: false })
    expect(vi.mocked(useUpload)).toHaveBeenCalledWith('w1', 'root')
  })

  it('uploads plainly when prediction is unavailable', async () => {
    const { input } = await arrange({ decision: null })
    await userEvent.upload(input, pdf())
    await userEvent.selectOptions(await screen.findByLabelText(/workspace/i), 'w2')
    await userEvent.click(screen.getByRole('button', { name: /^upload$/i }))

    await waitFor(() => expect(uploadFilesMock).toHaveBeenCalledTimes(1))
    const [, decisions] = uploadFilesMock.mock.calls[0]
    expect(decisions).toBeUndefined()
  })

  it('multi-file: decision applies to the first file only', async () => {
    const { input } = await arrange({ decision: DECISION })
    await userEvent.upload(input, [pdf('a.pdf'), pdf('b.pdf')])
    await userEvent.selectOptions(await screen.findByLabelText(/workspace/i), 'w1')
    await userEvent.click(screen.getByRole('button', { name: /^upload$/i }))

    await waitFor(() => expect(uploadFilesMock).toHaveBeenCalledTimes(1))
    const [files, decisions] = uploadFilesMock.mock.calls[0]
    expect(files).toHaveLength(2)
    expect(decisions).toHaveLength(2)
    expect(decisions[0]).toMatchObject({ predictionId: 'p1' })
    expect(decisions[1]).toBeNull()
  })
})
