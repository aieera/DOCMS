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
