import { describe, it, expect } from 'vitest'
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
