import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { UploadProgress } from '../UploadProgress'
import { useUploadStore } from '@/store/uploadStore'

// UX audit finding: useUpload's setStatus(id, 'failed', detail) already
// persists WHY an upload failed (virus scan rejection, policy refusal,
// humanized backend error — see useUpload.ts's catch block), but
// UploadProgress only ever rendered a bare red icon for a failed row.
// The reason was in the store the whole time; nothing read it.
//
// Retry is intentionally NOT asserted here: useUpload's uploadFiles()
// requires a workspaceId (it toasts and no-ops without one), and
// UploadItem does not carry the workspaceId/folderId an upload was
// started with — UploadProgress is mounted once, globally, aggregating
// rows from every workspace-scoped upload call site. Reaching a retry
// from here would mean adding that field to the store/UploadItem (out
// of this task's file scope, and a materially different fix). See the
// task report for the full reasoning.
describe('UploadProgress — failure visibility', () => {
  beforeEach(() => {
    useUploadStore.setState({ uploads: new Map() }, false)
  })

  it('shows the stored failure reason for a failed upload', () => {
    const file = new File(['%PDF-1.4'], 'contract.pdf', { type: 'application/pdf' })
    const { addUpload, setStatus } = useUploadStore.getState()

    addUpload({ id: 'u1', file, progress: 0, status: 'pending' })
    setStatus('u1', 'failed', 'Virus scan rejected the file')

    render(<UploadProgress />)

    expect(screen.getByText(/Virus scan rejected the file/i)).toBeInTheDocument()
  })
})
