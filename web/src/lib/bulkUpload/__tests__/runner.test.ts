import { it, expect, vi, beforeEach } from 'vitest'

const uploadFileToFolder = vi.fn()
const ensureFolder = vi.fn(async () => 'f1')
vi.mock('../folders', () => ({
  ensureWorkspace: vi.fn(async () => 'ws1'),
  resolveRootFolder: vi.fn(async () => 'root1'),
  makeFolderEnsurer: () => ensureFolder,
}))
vi.mock('../uploadFile', () => ({
  uploadFileToFolder: (...a: unknown[]) => uploadFileToFolder(...a),
}))

import { runImport } from '../runner'

const f = (n: string) => new File([new Uint8Array(2)], n.split('/').pop()!, { type: 'application/pdf' })

beforeEach(() => {
  uploadFileToFolder.mockReset()
  ensureFolder.mockClear()
})

it('uploads every file and reports a summary', async () => {
  uploadFileToFolder.mockResolvedValue({ documentId: 'd', deduplicated: false })
  const map = new Map([
    ['A/x.pdf', f('A/x.pdf')],
    ['A/y.pdf', f('A/y.pdf')],
  ])
  const res = await runImport({
    map,
    destination: { mode: 'existing', workspaceId: 'ws1' },
    conflict: 'new_version',
    concurrency: 2,
  })
  expect(res.created).toBe(2)
  expect(res.failed).toBe(0)
  expect(uploadFileToFolder).toHaveBeenCalledTimes(2)
})

it('records a per-file failure and continues the batch', async () => {
  uploadFileToFolder
    .mockRejectedValueOnce(new Error('scan blocked'))
    .mockResolvedValue({ documentId: 'd', deduplicated: false })
  const map = new Map([
    ['a.pdf', f('a.pdf')],
    ['b.pdf', f('b.pdf')],
  ])
  const res = await runImport({
    map,
    destination: { mode: 'existing', workspaceId: 'ws1' },
    conflict: 'new_version',
    concurrency: 1,
  })
  expect(res.created).toBe(1)
  expect(res.failed).toBe(1)
  expect(res.items.find((i) => i.outcome === 'failed')?.reason).toContain('scan blocked')
})

it('emits progress events', async () => {
  uploadFileToFolder.mockResolvedValue({ documentId: 'd', deduplicated: false })
  const events: string[] = []
  await runImport({
    map: new Map([['x.pdf', f('x.pdf')]]),
    destination: { mode: 'existing', workspaceId: 'ws1' },
    conflict: 'new_version',
    onEvent: (e) => events.push(e.state),
  })
  expect(events).toContain('uploading')
  expect(events).toContain('done')
})
