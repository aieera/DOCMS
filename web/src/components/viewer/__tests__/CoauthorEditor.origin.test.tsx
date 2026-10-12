import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn() }) }))
vi.mock('@/api/coauth', () => ({
  startCoauthSession: vi.fn(async () => ({
    provider: 'collabora',
    mode: 'edit',
    iframe_url: 'https://office.example.com/browser/cool.html?WOPISrc=x',
    editor_health_url: 'https://office.example.com/hosting/discovery',
  })),
  probeEditorReachable: vi.fn(async () => true),
}))

import { CoauthorEditor } from '@/components/viewer/CoauthorEditor'

const DOCX = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'
const close = JSON.stringify({ MessageId: 'Close_Session' })

async function openEditor() {
  render(
    <CoauthorEditor documentId="d" versionId="v" mimeType={DOCX} fileName="a.docx" downloadUrl="/dl" canEdit />,
  )
  fireEvent.click(screen.getByTestId('edit-in-browser'))
  await screen.findByTestId('coauth-modal')
  return screen.getByTestId('coauth-iframe') as HTMLIFrameElement
}

function post(data: string, origin: string, source: MessageEventSource | null) {
  act(() => {
    window.dispatchEvent(new MessageEvent('message', { data, origin, source }))
  })
}

// L-12: the co-authoring modal accepted Close_Session from ANY origin —
// any window (another tab, an ad iframe) could close the editor.
describe('CoauthorEditor message origin (L-12)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('ignores Close_Session from a foreign origin', async () => {
    const iframe = await openEditor()
    post(close, 'https://evil.example', iframe.contentWindow)
    expect(screen.getByTestId('coauth-modal')).toBeInTheDocument()
  })

  it('ignores Close_Session from the right origin but another window', async () => {
    await openEditor()
    post(close, 'https://office.example.com', window)
    expect(screen.getByTestId('coauth-modal')).toBeInTheDocument()
  })

  it('closes on Close_Session from the editor iframe', async () => {
    const iframe = await openEditor()
    post(close, 'https://office.example.com', iframe.contentWindow)
    expect(screen.queryByTestId('coauth-modal')).not.toBeInTheDocument()
  })
})
