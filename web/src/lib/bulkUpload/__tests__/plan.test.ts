import { describe, it, expect } from 'vitest'
import { buildPlan } from '../plan'

const f = (name: string, size = 10, type = 'application/pdf') =>
  new File([new Uint8Array(size)], name, { type })
const OPTS = { maxFileBytes: 100, blockedExt: ['exe', 'dll'] }

describe('buildPlan', () => {
  it('derives folders from paths and marks files ok', () => {
    const map = new Map([
      ['A/x.pdf', f('x.pdf')],
      ['A/B/y.pdf', f('y.pdf')],
    ])
    const { items, folderCount, fileCount } = buildPlan(map, OPTS)
    const folders = items.filter((i) => i.type === 'folder').map((i) => i.relPath).sort()
    expect(folders).toEqual(['A', 'A/B'])
    expect(folderCount).toBe(2)
    expect(fileCount).toBe(2)
    expect(items.filter((i) => i.type === 'file').every((i) => i.status === 'ok')).toBe(true)
  })

  it('flags oversize and blocked types', () => {
    const map = new Map([
      ['big.pdf', f('big.pdf', 500)],
      ['bad.exe', f('bad.exe', 5, 'application/x-msdownload')],
    ])
    const items = buildPlan(map, OPTS).items
    expect(items.find((i) => i.relPath === 'big.pdf')!.status).toBe('oversize')
    expect(items.find((i) => i.relPath === 'bad.exe')!.status).toBe('blocked_type')
  })

  it('sums total bytes across files', () => {
    const map = new Map([['a.pdf', f('a.pdf', 7)], ['b.pdf', f('b.pdf', 3)]])
    expect(buildPlan(map, OPTS).totalBytes).toBe(10)
  })
})
