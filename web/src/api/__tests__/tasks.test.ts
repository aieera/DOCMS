import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import {
  listTasks,
  listMyTasks,
  listMyCreatedTasks,
  createTask,
  addAssignee,
  removeAssignee,
  linkDocument,
  unlinkDocument,
  startTask,
  addComment,
  listActivity,
  taskKeys,
} from '@/api/tasks'

describe('api/tasks', () => {
  it('listTasks() returns the paginated envelope and forwards every filter', async () => {
    // GET /tasks is the only task endpoint that paginates; the envelope
    // shape is what the list UI binds to, so pin both it and the query
    // string the server sees.
    let seen = ''
    server.use(
      http.get('*/api/v1/tasks', ({ request }) => {
        seen = new URL(request.url).search
        return HttpResponse.json({
          items: [{ id: 't1', title: 'One', assignees: [], documents: [] }],
          total: 7,
          limit: 25,
          offset: 25,
        })
      }),
    )

    const page = await listTasks({
      filter: 'mine',
      status: 'open',
      priority: 'high',
      document_id: 'doc-1',
      q: 'invoice',
      include_completed: true,
      limit: 25,
      offset: 25,
      sort: 'priority',
    })

    expect(page.total).toBe(7)
    expect(page.items).toHaveLength(1)
    expect(seen).toContain('filter=mine')
    expect(seen).toContain('status=open')
    expect(seen).toContain('priority=high')
    expect(seen).toContain('document_id=doc-1')
    expect(seen).toContain('q=invoice')
    expect(seen).toContain('include_completed=true')
    expect(seen).toContain('limit=25')
    expect(seen).toContain('offset=25')
    expect(seen).toContain('sort=priority')
  })

  it('listTasks() omits absent filters rather than sending empty values', async () => {
    let seen = 'unset'
    server.use(
      http.get('*/api/v1/tasks', ({ request }) => {
        seen = new URL(request.url).search
        return HttpResponse.json({ items: [], total: 0, limit: 50, offset: 0 })
      }),
    )
    await listTasks({})
    expect(seen).toBe('')
  })

  it('listMyTasks/listMyCreatedTasks parse BARE ARRAYS, not the envelope', async () => {
    // /tasks/mine and /tasks/created are back-compat shims for the
    // mobile app and the topbar badge — they must keep returning arrays.
    server.use(
      http.get('*/api/v1/tasks/mine', () =>
        HttpResponse.json([{ id: 'a', title: 'Mine', assignees: [], documents: [] }]),
      ),
      http.get('*/api/v1/tasks/created', () =>
        HttpResponse.json([{ id: 'b', title: 'Created', assignees: [], documents: [] }]),
      ),
    )
    await expect(listMyTasks()).resolves.toHaveLength(1)
    await expect(listMyCreatedTasks()).resolves.toHaveLength(1)
  })

  it('createTask() posts multi-assignee and multi-document ids', async () => {
    let body: unknown
    server.use(
      http.post('*/api/v1/tasks', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ id: 'new', assignees: [], documents: [] }, { status: 201 })
      }),
    )
    await createTask({
      title: 'Review contracts',
      assignee_ids: ['u1', 'u2'],
      document_ids: ['d1', 'd2'],
    })
    expect(body).toMatchObject({
      title: 'Review contracts',
      assignee_ids: ['u1', 'u2'],
      document_ids: ['d1', 'd2'],
    })
  })

  it('assignee and document helpers hit the sub-resource routes', async () => {
    const calls: string[] = []
    const ok = () => HttpResponse.json({ id: 't1', assignees: [], documents: [] })
    server.use(
      http.post('*/api/v1/tasks/t1/assignees', ({ request }) => {
        calls.push(`POST ${new URL(request.url).pathname}`)
        return ok()
      }),
      http.delete('*/api/v1/tasks/t1/assignees/u9', ({ request }) => {
        calls.push(`DELETE ${new URL(request.url).pathname}`)
        return ok()
      }),
      http.post('*/api/v1/tasks/t1/documents', ({ request }) => {
        calls.push(`POST ${new URL(request.url).pathname}`)
        return ok()
      }),
      http.delete('*/api/v1/tasks/t1/documents/d9', ({ request }) => {
        calls.push(`DELETE ${new URL(request.url).pathname}`)
        return ok()
      }),
      http.post('*/api/v1/tasks/t1/start', ({ request }) => {
        calls.push(`POST ${new URL(request.url).pathname}`)
        return ok()
      }),
    )
    await addAssignee('t1', 'u9')
    await removeAssignee('t1', 'u9')
    await linkDocument('t1', 'd9')
    await unlinkDocument('t1', 'd9')
    await startTask('t1')
    expect(calls).toEqual([
      'POST /api/v1/tasks/t1/assignees',
      'DELETE /api/v1/tasks/t1/assignees/u9',
      'POST /api/v1/tasks/t1/documents',
      'DELETE /api/v1/tasks/t1/documents/d9',
      'POST /api/v1/tasks/t1/start',
    ])
  })

  it('addComment() posts the body and listActivity() returns entries', async () => {
    let body: unknown
    server.use(
      http.post('*/api/v1/tasks/t1/comments', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ id: 'c1', body: 'hi', mentions: [] }, { status: 201 })
      }),
      http.get('*/api/v1/tasks/t1/activity', () =>
        HttpResponse.json([{ id: 2, action: 'status_changed', detail: { from: 'open', to: 'done' } }]),
      ),
    )
    await addComment('t1', 'hi @[Ann](u1)')
    expect(body).toEqual({ body: 'hi @[Ann](u1)' })
    const activity = await listActivity('t1')
    expect(activity[0].action).toBe('status_changed')
  })

  it('every query key sits under the single ["tasks"] family', () => {
    // A shared prefix is what makes one invalidateTasks() refresh the
    // inboxes, the topbar badge, the dashboard card and the document
    // panel together — split keys were why the badge went stale.
    for (const key of [
      taskKeys.all,
      taskKeys.mine(),
      taskKeys.created(),
      taskKeys.list({ filter: 'all' }),
      taskKeys.detail('t1'),
      taskKeys.comments('t1'),
      taskKeys.activity('t1'),
    ]) {
      expect(key[0]).toBe('tasks')
    }
  })
})
