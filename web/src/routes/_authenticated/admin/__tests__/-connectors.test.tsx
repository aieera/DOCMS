// DEF-10: the admin Connectors page never showed an installed connector,
// and two of its Install buttons always failed.
//   1. GET /connectors returns a bare array of Connector rows whose type
//      field is `connector_type`; the page read `provider`, so no tile ever
//      showed "Installed".
//   2. The Google Drive tile asked for /connectors/google_drive/auth-url but
//      the backend only knows (and stores) `google`.
//   3. Salesforce has no backend provider, so Install always ended in a 400
//      "provider not yet supported" — it is Coming soon, like Dropbox / Box.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))

import { toast } from 'sonner'
import { ConnectorsPage } from '../connectors'

// A row exactly as services/connector/internal/model ConnectorConfig marshals.
const googleRow = {
  id: 'c1',
  tenant_id: 't1',
  connector_type: 'google',
  display_name: 'Google Workspace',
  is_active: true,
  last_sync_at: '2026-10-01T08:00:00Z',
  sync_status: 'ok',
  created_at: '2026-09-01T08:00:00Z',
  updated_at: '2026-10-01T08:00:00Z',
}

let listBody: unknown[]
let authUrlHits: string[]

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><ConnectorsPage /></QueryClientProvider>)
}

/** The catalogue card (the <li>) for a vendor, found by its visible label so
 *  the assertions do not depend on how the catalogue spells its ids. */
async function card(label: string) {
  const li = (await screen.findByRole('heading', { name: label })).closest('li') as HTMLElement
  return { li, btn: within(li).getByRole('button') }
}

describe('ConnectorsPage — installed state and provider ids (DEF-10)', () => {
  beforeEach(() => {
    listBody = [googleRow]
    authUrlHits = []
    vi.mocked(toast.message).mockClear()
    vi.mocked(toast.error).mockClear()
    server.use(
      http.get('*/api/v1/connectors', () => HttpResponse.json(listBody)),
      http.get('*/api/v1/connectors/:provider/auth-url', ({ params }) => {
        authUrlHits.push(String(params.provider))
        if (params.provider !== 'google') {
          return HttpResponse.json({ error: 'provider not yet supported: ' + params.provider }, { status: 400 })
        }
        // Real backend shape: {"auth_url": "..."} (handler.go getAuthURL).
        return HttpResponse.json({ auth_url: '#google-consent' })
      }),
    )
  })

  it('shows the installed badge on Google Drive only, for a connector_type "google" row', async () => {
    renderPage()
    const google = await card('Google Drive')
    await waitFor(() => expect(within(google.li).getByText('Installed')).toBeInTheDocument())

    for (const other of ['Salesforce', 'Microsoft 365', 'Dropbox', 'Box']) {
      const { li } = await card(other)
      expect(within(li).queryByText('Installed')).not.toBeInTheDocument()
    }
    // An installed tile offers re-authorisation rather than a fresh install.
    expect(google.btn).toHaveTextContent(/re-authorize/i)
  })

  it('shows no installed badge when nothing is connected', async () => {
    listBody = []
    renderPage()
    await card('Google Drive')
    expect(screen.queryByText('Installed')).not.toBeInTheDocument()
  })

  it('Google Drive Install requests the backend id /connectors/google/auth-url', async () => {
    listBody = []
    renderPage()
    const { btn } = await card('Google Drive')

    fireEvent.click(btn)
    await waitFor(() => expect(authUrlHits).toEqual(['google']))
    // The mutation settles (button leaves its loading/disabled state) with
    // no error toast: google_drive was a guaranteed 400 before.
    await waitFor(() => expect(btn).toBeEnabled())
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('follows the backend auth_url instead of falling into the "stub" toast', async () => {
    listBody = []
    renderPage()
    const { btn } = await card('Google Drive')
    fireEvent.click(btn)
    await waitFor(() => expect(authUrlHits).toEqual(['google']))
    await waitFor(() => expect(btn).toBeEnabled())
    expect(toast.message).not.toHaveBeenCalled()
  })

  it('Salesforce is Coming soon with a disabled Install button (no backend provider)', async () => {
    renderPage()
    const { btn, li } = await card('Salesforce')
    expect(within(li).getByText('Coming soon')).toBeInTheDocument()
    expect(btn).toBeDisabled()

    fireEvent.click(btn)
    expect(authUrlHits).toEqual([])
  })
})
