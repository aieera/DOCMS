import { describe, it, expect, vi } from 'vitest'
import { zipSync, strToU8 } from 'fflate'
import { parseFolderDrop, parseZip } from '../archive'

function fileWithRelPath(relPath: string): File {
  const file = new File(['x'], relPath.split('/').pop()!)
  Object.defineProperty(file, 'webkitRelativePath', { value: relPath })
  return file
}

describe('parseFolderDrop', () => {
  it('maps webkitRelativePath, strips the folder-picker root, drops junk', () => {
    const map = parseFolderDrop([
      fileWithRelPath('root/A/x.pdf'),
      fileWithRelPath('root/.DS_Store'),
    ])
    expect([...map.keys()]).toEqual(['A/x.pdf'])
  })
})

describe('parseZip', () => {
  it('reads entries into a path->File map (dirs + junk skipped)', async () => {
    const zipped = zipSync({
      'A/x.txt': strToU8('hello'),
      '__MACOSX/junk': strToU8('nope'),
    })
    const map = await parseZip(new File([zipped], 'b.zip'))
    expect([...map.keys()]).toEqual(['A/x.txt'])
    expect(await map.get('A/x.txt')!.text()).toBe('hello')
  })
})

// W-7: the ZIP used to be inflated whole in memory with no size gate.
describe('parseZip limits (W-7)', () => {
  it('oversize rejected before reading the archive', async () => {
    const f = new File([zipSync({ 'a.txt': strToU8('x') })], 'huge.zip')
    Object.defineProperty(f, 'size', { value: 2 * 1024 ** 3 + 1 })
    const read = vi.spyOn(f, 'arrayBuffer')
    const slice = vi.spyOn(f, 'slice')
    await expect(parseZip(f)).rejects.toThrow(/2 GiB/)
    expect(read).not.toHaveBeenCalled()
    expect(slice).not.toHaveBeenCalled()
  })

  it('rejects an entry that inflates past the per-entry cap', async () => {
    const zipped = zipSync({ 'bomb.txt': new Uint8Array(64 * 1024) }, { level: 9 })
    await expect(parseZip(new File([zipped], 'b.zip'), { maxEntryBytes: 1024 })).rejects.toThrow(/too large/i)
  })

  it('rejects an archive whose total inflated size passes the cap', async () => {
    const zipped = zipSync({ 'a.txt': new Uint8Array(800), 'b.txt': new Uint8Array(800) })
    await expect(parseZip(new File([zipped], 'b.zip'), { maxTotalBytes: 1000 })).rejects.toThrow(/too large/i)
  })

  it('rejects an archive with too many entries', async () => {
    const zipped = zipSync({ 'a.txt': strToU8('1'), 'b.txt': strToU8('2'), 'c.txt': strToU8('3') })
    await expect(parseZip(new File([zipped], 'b.zip'), { maxEntries: 2 })).rejects.toThrow(/too many/i)
  })

  it('rejects a corrupt archive instead of throwing unhandled', async () => {
    await expect(parseZip(new File([strToU8('not a zip at all')], 'bad.zip'))).rejects.toThrow()
  })

  it('rejects (does not hang on) a truncated archive', async () => {
    const zipped = zipSync({ 'a.txt': new Uint8Array(4096).map((_, i) => (i * 7919) % 251) }, { level: 0 })
    const cut = zipped.slice(0, Math.floor(zipped.length / 2))
    await expect(parseZip(new File([cut], 'cut.zip'))).rejects.toThrow()
    const deflated = zipSync({ 'b.txt': new Uint8Array(65536).map((_, i) => (i * 7919) % 251) }, { level: 9 })
    await expect(parseZip(new File([deflated.slice(0, 200)], 'cut2.zip'))).rejects.toThrow()
  }, 3000)

  it('still reads a normal multi-entry archive streamed in small chunks', async () => {
    const zipped = zipSync({ 'A/x.txt': strToU8('hello'), 'B/y.txt': strToU8('world') })
    const map = await parseZip(new File([zipped], 'ok.zip'), { chunkBytes: 7 })
    expect([...map.keys()].sort()).toEqual(['A/x.txt', 'B/y.txt'])
    expect(await map.get('B/y.txt')!.text()).toBe('world')
  })
})
