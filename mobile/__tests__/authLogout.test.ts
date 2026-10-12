// W-5: the document screen kept decrypted bytes in caches that survived
// logout (react-native-pdf's network cache, the download-and-share copy),
// so the next user on a shared device could reach the previous account's
// documents. Viewed/downloaded bytes now live in one app-owned directory
// that logout deletes.
const h = vi.hoisted(() => ({ deleted: [] as string[], made: [] as string[], downloads: [] as string[] }))

// Minimal zustand stand-in (the mobile deps are not installed in CI's web
// toolchain); the store logic under test only uses create/set/getState.
vi.mock('zustand', () => ({
  create: (init: (set: (p: object) => void, get: () => object) => object) => {
    let state: Record<string, unknown> = {}
    const set = (p: object | ((s: object) => object)) => {
      state = { ...state, ...(typeof p === 'function' ? p(state) : p) }
    }
    const get = () => state
    state = init(set, get) as Record<string, unknown>
    const hook = () => state
    return Object.assign(hook, { getState: get, setState: set })
  },
}))
vi.mock('expo-secure-store', () => ({
  setItemAsync: vi.fn(async () => {}),
  getItemAsync: vi.fn(async () => null),
  deleteItemAsync: vi.fn(async () => {}),
}))
vi.mock('../lib/queryClient', () => ({ clearQueryCache: vi.fn(async () => {}) }))
vi.mock('expo-file-system', () => ({
  cacheDirectory: 'file:///cache/',
  deleteAsync: vi.fn(async (p: string) => { h.deleted.push(p) }),
  makeDirectoryAsync: vi.fn(async (p: string) => { h.made.push(p) }),
  getInfoAsync: vi.fn(async () => ({ exists: false })),
  downloadAsync: vi.fn(async (_url: string, dest: string) => { h.downloads.push(dest); return { status: 200, uri: dest } }),
}))

import { useAuthStore } from '../store/authStore'
import { DOC_CACHE_DIR, localDocumentCopy, wipeDocCache } from '../lib/docCache'
import { clearQueryCache } from '../lib/queryClient'

beforeEach(() => { h.deleted.length = 0; h.made.length = 0; h.downloads.length = 0 })

describe('document byte cache (W-5)', () => {
  it('downloads viewed documents into the app-owned cache directory', async () => {
    const uri = await localDocumentCopy({ url: 'https://api/x/content', documentID: 'd/1', mime: 'application/pdf', token: 't' })
    expect(uri.startsWith(DOC_CACHE_DIR)).toBe(true)
    expect(h.downloads[0].startsWith(DOC_CACHE_DIR)).toBe(true)
    expect(h.downloads[0]).not.toContain('d/1') // id is path-safe
  })

  it('wipeDocCache deletes the whole directory', async () => {
    await wipeDocCache()
    expect(h.deleted).toContain(DOC_CACHE_DIR)
  })

  it('logout wipes the document cache and the query cache', async () => {
    await useAuthStore.getState().login({ id: 'u', email: 'a@x', display_name: 'A', role: 'member', tenant_id: 't' }, 'tok', 't')
    await useAuthStore.getState().logout()
    expect(h.deleted).toContain(DOC_CACHE_DIR)
    expect(clearQueryCache).toHaveBeenCalled()
    expect(useAuthStore.getState().token).toBeNull()
  })
})
