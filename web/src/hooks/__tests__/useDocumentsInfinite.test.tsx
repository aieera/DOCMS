// Cursor pagination for the workspace/folder browser.
//
// Two failures this locks down, both of which shipped:
//   1. No pagination at all — the browser rendered only the first page
//      (20 rows) with no pager, so document #21+ in a folder was
//      unreachable. Ingesting a mailbox put 28 files in one folder and the
//      attachment the user was looking for sat at #25.
//   2. The wrong param name. page_token lives inside the nested
//      `pagination` message, so grpc-gateway binds it as
//      `pagination.page_token`; a plain `?page_token=` is silently ignored
//      and the server replays page 1 — identical rows forever.

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { useDocumentsInfinite, documentsFromPages } from '../useDocuments'

const getDocuments = vi.hoisted(() => vi.fn())
vi.mock('@/api/documents', () => ({
  getDocuments,
  getDocument: vi.fn(),
  updateDocument: vi.fn(),
  deleteDocument: vi.fn(),
  moveDocument: vi.fn(),
  copyDocument: vi.fn(),
}))

const wrapper = ({ children }: { children: ReactNode }) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const page = (ids: string[], next: string) => ({
  documents: ids.map((id) => ({ id, title: `${id}.pdf` })),
  pagination: { next_page_token: next, total_count: '-1' },
})

beforeEach(() => getDocuments.mockReset())

describe('useDocumentsInfinite', () => {
  it('sends the cursor as pagination.page_token, not page_token', async () => {
    getDocuments
      .mockResolvedValueOnce(page(['a'], 'CURSOR1'))
      .mockResolvedValueOnce(page(['b'], ''))

    const { result } = renderHook(() => useDocumentsInfinite('ws1', { folder_id: 'f1' }), { wrapper })
    await waitFor(() => expect(result.current.hasNextPage).toBe(true))

    result.current.fetchNextPage()
    await waitFor(() => expect(getDocuments).toHaveBeenCalledTimes(2))

    const secondCallParams = getDocuments.mock.calls[1][1]
    expect(secondCallParams).toEqual({ folder_id: 'f1', 'pagination.page_token': 'CURSOR1' })
    // The spelling grpc-gateway drops on the floor.
    expect(secondCallParams).not.toHaveProperty('page_token')
  })

  it('stops when the server returns an empty token', async () => {
    getDocuments.mockResolvedValueOnce(page(['a'], ''))
    const { result } = renderHook(() => useDocumentsInfinite('ws1'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.hasNextPage).toBe(false)
  })

  it('stops when the token does not advance, instead of looping forever', async () => {
    // A server that echoes the same cursor must not produce an unbounded
    // fetch loop appending duplicate rows.
    getDocuments
      .mockResolvedValueOnce(page(['a'], 'SAME'))
      .mockResolvedValueOnce(page(['b'], 'SAME'))

    const { result } = renderHook(() => useDocumentsInfinite('ws1'), { wrapper })
    await waitFor(() => expect(result.current.hasNextPage).toBe(true))

    result.current.fetchNextPage()
    await waitFor(() => expect(result.current.data?.pages).toHaveLength(2))
    await waitFor(() => expect(result.current.hasNextPage).toBe(false))
  })

  it('does not fetch without a workspace id', () => {
    renderHook(() => useDocumentsInfinite(''), { wrapper })
    expect(getDocuments).not.toHaveBeenCalled()
  })
})

describe('documentsFromPages', () => {
  it('flattens pages in order', () => {
    const rows = documentsFromPages([page(['a', 'b'], 'x'), page(['c'], '')])
    expect(rows.map((r) => (r as { id: string }).id)).toEqual(['a', 'b', 'c'])
  })

  it('reads the legacy {items} shape too', () => {
    expect(documentsFromPages([{ items: [{ id: 'z' }] }])).toEqual([{ id: 'z' }])
  })

  it('is empty for undefined', () => {
    expect(documentsFromPages(undefined)).toEqual([])
  })
})
