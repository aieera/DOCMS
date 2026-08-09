import { describe, it, expect } from 'vitest'
import { documentTitleFor, formatDocumentTitle, pageNameFor } from '@/lib/documentTitle'

// Every route used to render the bare app name in the tab, the history
// entry and the screen-reader page announcement.
describe('documentTitleFor', () => {
  it('names the page and keeps the app suffix', () => {
    expect(documentTitleFor([{ routeId: '/_authenticated/tasks' }], '/tasks')).toBe('Tasks · SeDoc')
  })

  it('titles the dashboard "Home", not the bare app name', () => {
    expect(documentTitleFor([{ routeId: '/_authenticated/' }], '/')).toBe('Home · SeDoc')
  })

  it('derives admin pages from the URL via the shared label table', () => {
    expect(documentTitleFor([{ routeId: '/_authenticated/admin/audit-log' }], '/admin/audit-log'))
      .toBe('Audit log · SeDoc')
    expect(documentTitleFor([{ routeId: '/_authenticated/admin/api-keys' }], '/admin/api-keys'))
      .toBe('API keys · SeDoc')
  })

  it('never leaks an opaque id into the title', () => {
    const title = documentTitleFor(
      [
        { routeId: '/_authenticated' },
        { routeId: '/_authenticated/workspaces/$workspaceId/documents/$documentId' },
      ],
      '/workspaces/6e11d682-ec67-43a1-9cce-729f5062aa84/documents/0f5e2261-ec67-43a1-9cce-729f5062aa84',
    )
    expect(title).toBe('Document · SeDoc')
  })

  it('maps dynamic routes that the URL cannot name', () => {
    expect(documentTitleFor([{ routeId: '/shared/$token' }], '/shared/abc123')).toBe('Shared document · SeDoc')
    expect(documentTitleFor([{ routeId: '/login' }], '/login')).toBe('Sign in · SeDoc')
  })

  it("lets a route's own head() meta title win, deepest first", () => {
    const title = documentTitleFor(
      [
        { routeId: '/_authenticated' },
        { routeId: '/_authenticated/tasks', meta: [{ title: 'Acme MSA 2026' }] },
      ],
      '/tasks',
    )
    expect(title).toBe('Acme MSA 2026 · SeDoc')
  })

  it('falls back to the app name when nothing can be resolved', () => {
    expect(formatDocumentTitle(undefined)).toBe('SeDoc')
    expect(formatDocumentTitle('  ')).toBe('SeDoc')
    expect(pageNameFor([], '/')).toBe('Home')
  })
})
