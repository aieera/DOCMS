import { it, expect, vi, beforeEach } from 'vitest'

const createFolder = vi.fn()
const getFolders = vi.fn()
const createWorkspace = vi.fn(async (name: string) => ({ id: 'ws-new', name }))
vi.mock('@/api/workspaces', () => ({
  createWorkspace: (...a: unknown[]) => createWorkspace(...(a as [string])),
  getFolders: (...a: unknown[]) => getFolders(...a),
  createFolder: (...a: unknown[]) => createFolder(...a),
}))

import { makeFolderEnsurer, ensureWorkspace } from '../folders'

beforeEach(() => {
  getFolders.mockReset()
  createFolder.mockReset()
  createWorkspace.mockClear()
})

it('ensureWorkspace returns the existing id when mode=existing', async () => {
  expect(await ensureWorkspace({ mode: 'existing', workspaceId: 'ws1' })).toBe('ws1')
  expect(createWorkspace).not.toHaveBeenCalled()
})

it('ensureWorkspace creates a new workspace when mode=new', async () => {
  expect(await ensureWorkspace({ mode: 'new', newWorkspaceName: 'Legal' })).toBe('ws-new')
  expect(createWorkspace).toHaveBeenCalledWith('Legal')
})

it('creates each missing segment once and caches', async () => {
  getFolders.mockResolvedValue([]) // nothing exists yet
  createFolder.mockImplementation(async (_ws: string, name: string) => ({ id: `f-${name}`, name }))
  const ensure = makeFolderEnsurer('ws1')
  expect(await ensure('A/B')).toBe('f-B')
  await ensure('A/B') // cached — no new calls
  expect(createFolder).toHaveBeenCalledTimes(2) // A + B, once total
})

it('reuses an existing same-name child instead of recreating', async () => {
  getFolders.mockImplementation(async (_ws: string, parent?: string) =>
    parent ? [] : [{ id: 'f-A', name: 'A' }])
  createFolder.mockImplementation(async (_ws: string, name: string) => ({ id: `f-${name}`, name }))
  const ensure = makeFolderEnsurer('ws1')
  expect(await ensure('A')).toBe('f-A')
  expect(createFolder).not.toHaveBeenCalled()
})

// W-3: the runner uses 4 workers. Two files in the same new directory
// used to race through ensure() before either cached its id, so both
// saw "no sibling" and created duplicate sibling folders.
it('concurrent ensure creates one folder', async () => {
  getFolders.mockResolvedValue([])
  let n = 0
  createFolder.mockImplementation(async (_ws: string, name: string) => {
    await new Promise((r) => setTimeout(r, 5))
    return { id: `f-${name}-${++n}`, name }
  })
  const ensure = makeFolderEnsurer('ws1', 'root')
  const ids = await Promise.all([ensure('A/B'), ensure('A/B'), ensure('A/C'), ensure('A')])
  expect(createFolder.mock.calls.filter((c) => c[1] === 'A')).toHaveLength(1)
  expect(createFolder.mock.calls.filter((c) => c[1] === 'B')).toHaveLength(1)
  expect(ids[0]).toBe(ids[1])
  expect(createFolder).toHaveBeenCalledTimes(3) // A, B, C
})

it('a failed ensure is not cached, so a retry can succeed', async () => {
  getFolders.mockResolvedValue([])
  createFolder
    .mockRejectedValueOnce(new Error('transient'))
    .mockImplementation(async (_ws: string, name: string) => ({ id: `f-${name}`, name }))
  const ensure = makeFolderEnsurer('ws1', 'root')
  await expect(ensure('A')).rejects.toThrow('transient')
  expect(await ensure('A')).toBe('f-A')
})
