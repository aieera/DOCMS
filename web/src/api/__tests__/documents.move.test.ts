// Regression test for the drag-and-drop move.
//
// Dragging a file onto a folder appeared broken in the UI: the overlay
// showed, the drop target highlighted, and POST /documents/{id}/move
// fired — then the server answered 400 "required" and the move rolled
// back. The drag machinery was never at fault. The client sent
// `{ folder_id }`, but MoveDocumentRequest names the field
// `target_folder_id` (proto/sedoc/v1/document.proto:258-262), so the
// gateway dropped the unknown key and the required field arrived empty.
// Verified against the live API: `{folder_id: …}` returns exactly the
// same error as an empty body.
//
// copyDocument, three lines below it, always sent `target_folder_id`
// correctly — which is why copying worked and moving did not.
import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'

import { moveDocument, copyDocument } from '@/api/documents'

describe('api/documents move & copy', () => {
  it('moveDocument sends target_folder_id, the field the proto declares', async () => {
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/documents/:id/move', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ id: 'd1' })
      }),
    )

    await moveDocument('d1', 'folder-42')

    expect(body).toEqual({ target_folder_id: 'folder-42' })
    // The old spelling is silently ignored by the gateway, so sending it
    // looks like a working request and fails server-side.
    expect(body.folder_id).toBeUndefined()
  })

  it('copyDocument sends target_folder_id too — the pair must not drift again', async () => {
    let body: Record<string, unknown> = {}
    server.use(
      http.post('*/api/v1/documents/:id/copy', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json({ id: 'd2' })
      }),
    )

    await copyDocument('d1', 'folder-42')

    expect(body).toEqual({ target_folder_id: 'folder-42' })
  })
})
