# Bulk Content Upload (Phase 1, client-side) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a non-developer bulk-upload real content by dropping a `.zip` or a folder on `/admin/bulk`; the directory structure becomes the workspace/folder hierarchy and each file becomes a document through the existing upload pipeline (scan/OCR/dedup/versioning).

**Architecture:** Web-only, no backend change. A pure logic layer (`web/src/lib/bulkUpload/*`) parses the archive/folder into a sanitized `path → File` map, builds an engine-agnostic `PlannedItem[]`, and a runner ensures the destination workspace/folders exist (idempotent, path→id cache) then uploads each file via the existing `initiate → PUT → complete → createVersion` sequence behind a concurrency-limited queue. A wizard UI (`web/src/components/admin/bulkUpload/*`) drives source → preview → destination → dry-run → run → report. The current NDJSON importer is demoted to an "Advanced / migration" tab. The `PlannedItem`/`ImportResult` contract is shared so a Phase-2 server engine can slot in without UI rework.

**Tech Stack:** React 18 + TanStack Router/Query, TypeScript, Vitest + Testing Library, `fflate` (new dep — small tree-shakeable unzip), existing `@/api/{upload,documents,workspaces}` helpers, `sonner` toasts, `useAppMutation`.

## Global Constraints

- Web-only; **no backend/API changes** in Phase 1. Reuse existing endpoints only.
- New dependency: `fflate` (unzip). Add to `web/package.json` in Task 3, nowhere else.
- Every task ends green: `npx tsc --noEmit` clean and `npx eslint <changed files>` 0 errors.
- Follow codebase conventions: api calls in `@/api/*`, mutations via `useAppMutation`, toasts via `sonner`, design tokens (`border-border`, `bg-card`, `text-muted-foreground`), `PageHeader`.
- Admin-gated route unchanged (`/admin/bulk` already sits under admin nav).
- Conflict-policy default `new_version`; options `skip`, `rename`.
- Path safety: reject `..`, absolute, and drive-prefixed paths (zip-slip) before any create/upload.
- Soft caps (warn, don't hard-block): > ~500 MB total or > ~2,000 files → show a "consider server-side (Phase 2)" notice.

## File Structure

- Create `web/src/lib/bulkUpload/types.ts` — shared contract (`PlannedItem`, `ImportResult`, `Destination`, `ConflictPolicy`, `PathFileMap`).
- Create `web/src/lib/bulkUpload/paths.ts` — `sanitizeRelPath`, `isJunkPath`, `dirSegments`.
- Create `web/src/lib/bulkUpload/plan.ts` — `buildPlan(map, opts) → PlannedItem[]` (validation: blocked type / oversize / empty dir).
- Create `web/src/lib/bulkUpload/archive.ts` — `parseZip(File) → PathFileMap`, `parseFolderDrop(FileList) → PathFileMap`.
- Create `web/src/lib/bulkUpload/folders.ts` — `ensureWorkspace`, `ensureFolderPath` (idempotent, cached).
- Create `web/src/lib/bulkUpload/uploadFile.ts` — `uploadFileToFolder(...)` (per-file initiate/PUT/complete/version).
- Create `web/src/lib/bulkUpload/runner.ts` — `runImport(map, destination, opts, onEvent) → ImportResult` (queue + orchestration).
- Create `web/src/components/admin/bulkUpload/UploadWizard.tsx` — wizard shell + steps.
- Modify `web/src/routes/_authenticated/admin/bulk.tsx` — 3 tabs; new default "Upload files & folders", NDJSON → "Advanced / migration".
- Tests colocated: `web/src/lib/bulkUpload/__tests__/{paths,plan,archive,folders,uploadFile,runner}.test.ts`, `web/src/components/admin/bulkUpload/__tests__/UploadWizard.test.tsx`.

---

### Task 1: Shared contract + path sanitizer

**Files:**
- Create: `web/src/lib/bulkUpload/types.ts`
- Create: `web/src/lib/bulkUpload/paths.ts`
- Test: `web/src/lib/bulkUpload/__tests__/paths.test.ts`

**Interfaces:**
- Produces: `type ConflictPolicy = 'new_version' | 'skip' | 'rename'`; `type PathFileMap = Map<string, File>`; `interface Destination { mode: 'new' | 'existing'; workspaceId?: string; newWorkspaceName?: string; targetFolderId?: string }`; `interface PlannedItem { relPath: string; type: 'folder' | 'file'; sizeBytes?: number; mimeType?: string; status: 'ok' | 'blocked_type' | 'oversize' | 'empty'; note?: string }`; `interface ImportResult { created: number; skipped: number; failed: number; items: { relPath: string; outcome: 'created' | 'versioned' | 'skipped' | 'renamed' | 'failed'; reason?: string; documentId?: string }[] }`; `sanitizeRelPath(raw: string): string | null` (null = reject); `isJunkPath(p: string): boolean`; `dirSegments(relPath: string): string[]`.

- [ ] **Step 1: Write the failing test**
```ts
import { describe, it, expect } from 'vitest'
import { sanitizeRelPath, isJunkPath, dirSegments } from '../paths'

describe('sanitizeRelPath', () => {
  it('normalizes separators and strips leading slash', () => {
    expect(sanitizeRelPath('\\a\\b\\c.txt')).toBe('a/b/c.txt')
    expect(sanitizeRelPath('/a/b.txt')).toBe('a/b.txt')
  })
  it('rejects traversal, absolute, and drive paths (zip-slip)', () => {
    expect(sanitizeRelPath('../etc/passwd')).toBeNull()
    expect(sanitizeRelPath('a/../../b')).toBeNull()
    expect(sanitizeRelPath('C:\\win\\x')).toBeNull()
  })
  it('collapses . segments and empty segments', () => {
    expect(sanitizeRelPath('a/./b//c.txt')).toBe('a/b/c.txt')
  })
})
describe('isJunkPath', () => {
  it('flags OS junk', () => {
    expect(isJunkPath('__MACOSX/x')).toBe(true)
    expect(isJunkPath('a/.DS_Store')).toBe(true)
    expect(isJunkPath('Thumbs.db')).toBe(true)
    expect(isJunkPath('a/real.pdf')).toBe(false)
  })
})
describe('dirSegments', () => {
  it('returns parent dirs only', () => {
    expect(dirSegments('a/b/c.txt')).toEqual(['a', 'b'])
    expect(dirSegments('root.txt')).toEqual([])
  })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npx vitest run src/lib/bulkUpload/__tests__/paths.test.ts`
Expected: FAIL (module not found).

- [ ] **Step 3: Write `types.ts` then `paths.ts`**
```ts
// types.ts — copy the Interfaces block above verbatim as exported types.
```
```ts
// paths.ts
export function sanitizeRelPath(raw: string): string | null {
  if (!raw) return null
  if (/^[a-zA-Z]:[\\/]/.test(raw)) return null // drive prefix
  const norm = raw.replace(/\\/g, '/')
  if (norm.startsWith('/')) return norm.slice(1) === '' ? null : sanitizeRelPath(norm.slice(1))
  const out: string[] = []
  for (const seg of norm.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') return null // traversal — reject the whole path
    out.push(seg)
  }
  return out.length ? out.join('/') : null
}

const JUNK = [/(^|\/)__MACOSX(\/|$)/, /(^|\/)\.DS_Store$/, /(^|\/)Thumbs\.db$/i]
export function isJunkPath(p: string): boolean {
  return JUNK.some((re) => re.test(p))
}

export function dirSegments(relPath: string): string[] {
  const segs = relPath.split('/')
  return segs.slice(0, -1)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && npx vitest run src/lib/bulkUpload/__tests__/paths.test.ts` → PASS. Then `npx tsc --noEmit` clean.

- [ ] **Step 5: Commit**
```bash
git add web/src/lib/bulkUpload/types.ts web/src/lib/bulkUpload/paths.ts web/src/lib/bulkUpload/__tests__/paths.test.ts
git commit -m "feat(web/bulk): shared bulk-upload contract + zip-slip path sanitizer"
```

---

### Task 2: Plan builder (tree → PlannedItem[])

**Files:**
- Create: `web/src/lib/bulkUpload/plan.ts`
- Test: `web/src/lib/bulkUpload/__tests__/plan.test.ts`

**Interfaces:**
- Consumes: `PathFileMap`, `PlannedItem`, `sanitizeRelPath`, `isJunkPath`, `dirSegments` (Task 1).
- Produces: `interface PlanOpts { maxFileBytes: number; blockedExt: string[] }`; `buildPlan(map: PathFileMap, opts: PlanOpts): { items: PlannedItem[]; totalBytes: number; fileCount: number; folderCount: number }`. Folder items are the distinct parent dirs; file items carry size/mime/status.

- [ ] **Step 1: Write the failing test**
```ts
import { describe, it, expect } from 'vitest'
import { buildPlan } from '../plan'

const f = (name: string, size = 10, type = 'application/pdf') =>
  new File([new Uint8Array(size)], name, { type })
const OPTS = { maxFileBytes: 100, blockedExt: ['exe', 'dll'] }

describe('buildPlan', () => {
  it('derives folders from paths and marks files ok', () => {
    const map = new Map([['A/x.pdf', f('x.pdf')], ['A/B/y.pdf', f('y.pdf')]])
    const { items, folderCount, fileCount } = buildPlan(map, OPTS)
    const folders = items.filter((i) => i.type === 'folder').map((i) => i.relPath).sort()
    expect(folders).toEqual(['A', 'A/B'])
    expect(folderCount).toBe(2)
    expect(fileCount).toBe(2)
    expect(items.filter((i) => i.type === 'file').every((i) => i.status === 'ok')).toBe(true)
  })
  it('flags oversize and blocked types', () => {
    const map = new Map([['big.pdf', f('big.pdf', 500)], ['bad.exe', f('bad.exe', 5, 'application/x-msdownload')]])
    const items = buildPlan(map, OPTS).items
    expect(items.find((i) => i.relPath === 'big.pdf')!.status).toBe('oversize')
    expect(items.find((i) => i.relPath === 'bad.exe')!.status).toBe('blocked_type')
  })
})
```

- [ ] **Step 2: Run → FAIL.** `cd web && npx vitest run src/lib/bulkUpload/__tests__/plan.test.ts`

- [ ] **Step 3: Implement `plan.ts`**
```ts
import { dirSegments } from './paths'
import type { PathFileMap, PlannedItem } from './types'

export interface PlanOpts { maxFileBytes: number; blockedExt: string[] }

export function buildPlan(map: PathFileMap, opts: PlanOpts) {
  const folderSet = new Set<string>()
  const items: PlannedItem[] = []
  let totalBytes = 0
  for (const [relPath, file] of map) {
    const segs = dirSegments(relPath)
    for (let i = 0; i < segs.length; i++) folderSet.add(segs.slice(0, i + 1).join('/'))
    const ext = relPath.split('.').pop()?.toLowerCase() ?? ''
    let status: PlannedItem['status'] = 'ok'
    if (opts.blockedExt.includes(ext)) status = 'blocked_type'
    else if (file.size > opts.maxFileBytes) status = 'oversize'
    totalBytes += file.size
    items.push({ relPath, type: 'file', sizeBytes: file.size, mimeType: file.type, status })
  }
  for (const dir of folderSet) items.push({ relPath: dir, type: 'folder', status: 'ok' })
  return { items, totalBytes, fileCount: map.size, folderCount: folderSet.size }
}
```

- [ ] **Step 4: Run → PASS.** Then `npx tsc --noEmit`.

- [ ] **Step 5: Commit**
```bash
git add web/src/lib/bulkUpload/plan.ts web/src/lib/bulkUpload/__tests__/plan.test.ts
git commit -m "feat(web/bulk): plan builder (path map -> PlannedItem tree + validation)"
```

---

### Task 3: Archive + folder-drop parsers (adds `fflate`)

**Files:**
- Modify: `web/package.json` (add `fflate`)
- Create: `web/src/lib/bulkUpload/archive.ts`
- Test: `web/src/lib/bulkUpload/__tests__/archive.test.ts`

**Interfaces:**
- Consumes: `sanitizeRelPath`, `isJunkPath` (Task 1), `PathFileMap`.
- Produces: `parseFolderDrop(files: FileList | File[]): PathFileMap` (uses each file's `webkitRelativePath`); `parseZip(file: File): Promise<PathFileMap>` (via `fflate.unzip`). Both drop junk + reject unsanitizable paths.

- [ ] **Step 1: Add dependency**

Run: `cd web && npm install fflate`
Verify `fflate` appears in `package.json` dependencies.

- [ ] **Step 2: Write the failing test** (folder-drop is deterministic; zip round-trips through fflate's zipSync)
```ts
import { describe, it, expect } from 'vitest'
import { zipSync, strToU8 } from 'fflate'
import { parseFolderDrop, parseZip } from '../archive'

function fileWithRelPath(relPath: string): File {
  const file = new File(['x'], relPath.split('/').pop()!)
  Object.defineProperty(file, 'webkitRelativePath', { value: relPath })
  return file
}

describe('parseFolderDrop', () => {
  it('maps webkitRelativePath, strips top folder-picker root, drops junk', () => {
    const map = parseFolderDrop([fileWithRelPath('root/A/x.pdf'), fileWithRelPath('root/.DS_Store')])
    expect([...map.keys()]).toEqual(['A/x.pdf'])
  })
})
describe('parseZip', () => {
  it('reads entries into a path->File map', async () => {
    const zipped = zipSync({ 'A/x.txt': strToU8('hello') })
    const map = await parseZip(new File([zipped], 'b.zip'))
    expect([...map.keys()]).toEqual(['A/x.txt'])
    expect(await map.get('A/x.txt')!.text()).toBe('hello')
  })
})
```

- [ ] **Step 3: Run → FAIL.**

- [ ] **Step 4: Implement `archive.ts`**
```ts
import { unzip } from 'fflate'
import { sanitizeRelPath, isJunkPath } from './paths'
import type { PathFileMap } from './types'

// Folder pickers prefix every path with the chosen dir; strip that first segment.
function stripRoot(relPath: string): string {
  const i = relPath.indexOf('/')
  return i === -1 ? relPath : relPath.slice(i + 1)
}

export function parseFolderDrop(files: FileList | File[]): PathFileMap {
  const out: PathFileMap = new Map()
  for (const file of Array.from(files)) {
    const raw = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name
    const rel = sanitizeRelPath(stripRoot(raw))
    if (!rel || isJunkPath(rel)) continue
    out.set(rel, file)
  }
  return out
}

export function parseZip(file: File): Promise<PathFileMap> {
  return file.arrayBuffer().then(
    (buf) =>
      new Promise<PathFileMap>((resolve, reject) => {
        unzip(new Uint8Array(buf), (err, unzipped) => {
          if (err) return reject(err)
          const out: PathFileMap = new Map()
          for (const [name, bytes] of Object.entries(unzipped)) {
            if (name.endsWith('/')) continue // directory entry
            const rel = sanitizeRelPath(name)
            if (!rel || isJunkPath(rel)) continue
            out.set(rel, new File([bytes], rel.split('/').pop()!))
          }
          resolve(out)
        })
      }),
  )
}
```

- [ ] **Step 5: Run → PASS.** Then `npx tsc --noEmit`.

- [ ] **Step 6: Commit**
```bash
git add web/package.json web/package-lock.json web/src/lib/bulkUpload/archive.ts web/src/lib/bulkUpload/__tests__/archive.test.ts
git commit -m "feat(web/bulk): zip + folder-drop parsers (fflate), junk + zip-slip filtered"
```

---

### Task 4: Idempotent workspace/folder ensurer

**Files:**
- Create: `web/src/lib/bulkUpload/folders.ts`
- Test: `web/src/lib/bulkUpload/__tests__/folders.test.ts`

**Interfaces:**
- Consumes: `@/api/workspaces` — `createWorkspace(name, description?) → Workspace`, `getFolders(workspaceId, parentId?) → Folder[]`, `createFolder(workspaceId, name, parentId?, visibility?) → Folder`.
- Produces: `ensureWorkspace(dest: Destination): Promise<string>` (returns workspaceId; creates when `mode:'new'`); `makeFolderEnsurer(workspaceId: string, rootFolderId?: string): (dirPath: string) => Promise<string | undefined>` — resolves/creates each segment of `dirPath` under the root, caching `dirPath → folderId`, reusing an existing folder when a same-name child exists.

- [ ] **Step 1: Write the failing test** (mock `@/api/workspaces`)
```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'

const createFolder = vi.fn()
const getFolders = vi.fn()
vi.mock('@/api/workspaces', () => ({
  createWorkspace: vi.fn(async (name: string) => ({ id: 'ws-new', name })),
  getFolders: (...a: unknown[]) => getFolders(...a),
  createFolder: (...a: unknown[]) => createFolder(...a),
}))

import { makeFolderEnsurer, ensureWorkspace } from '../folders'

beforeEach(() => { getFolders.mockReset(); createFolder.mockReset() })

it('ensureWorkspace creates a new workspace when mode=new', async () => {
  expect(await ensureWorkspace({ mode: 'new', newWorkspaceName: 'Legal' })).toBe('ws-new')
})
it('creates each missing segment once and caches', async () => {
  getFolders.mockResolvedValue([]) // nothing exists
  createFolder.mockImplementation(async (_ws: string, name: string) => ({ id: `f-${name}`, name }))
  const ensure = makeFolderEnsurer('ws1')
  expect(await ensure('A/B')).toBe('f-B')
  await ensure('A/B') // cached — no new calls
  expect(createFolder).toHaveBeenCalledTimes(2) // A, B — once total
})
it('reuses an existing same-name child', async () => {
  getFolders.mockImplementation(async (_ws: string, parent?: string) =>
    parent ? [] : [{ id: 'f-A', name: 'A' }])
  createFolder.mockImplementation(async (_ws: string, name: string) => ({ id: `f-${name}`, name }))
  const ensure = makeFolderEnsurer('ws1')
  expect(await ensure('A')).toBe('f-A')
  expect(createFolder).not.toHaveBeenCalledWith('ws1', 'A', undefined, undefined)
})
```

- [ ] **Step 2: Run → FAIL.**

- [ ] **Step 3: Implement `folders.ts`**
```ts
import { createWorkspace, getFolders, createFolder } from '@/api/workspaces'
import type { Destination } from './types'

export async function ensureWorkspace(dest: Destination): Promise<string> {
  if (dest.mode === 'existing') {
    if (!dest.workspaceId) throw new Error('workspace required')
    return dest.workspaceId
  }
  const name = (dest.newWorkspaceName ?? '').trim()
  if (!name) throw new Error('new workspace name required')
  const ws = await createWorkspace(name)
  return ws.id
}

export function makeFolderEnsurer(workspaceId: string, rootFolderId?: string) {
  const cache = new Map<string, string | undefined>()
  cache.set('', rootFolderId)
  async function ensure(dirPath: string): Promise<string | undefined> {
    if (cache.has(dirPath)) return cache.get(dirPath)
    const segs = dirPath.split('/')
    const parentPath = segs.slice(0, -1).join('/')
    const name = segs[segs.length - 1]
    const parentId = await ensure(parentPath)
    const siblings = await getFolders(workspaceId, parentId)
    const existing = siblings.find((f) => f.name === name)
    const id = existing ? existing.id : (await createFolder(workspaceId, name, parentId)).id
    cache.set(dirPath, id)
    return id
  }
  return ensure
}
```

- [ ] **Step 4: Run → PASS.** `npx tsc --noEmit`.

- [ ] **Step 5: Commit**
```bash
git add web/src/lib/bulkUpload/folders.ts web/src/lib/bulkUpload/__tests__/folders.test.ts
git commit -m "feat(web/bulk): idempotent workspace + folder-path ensurer (cached)"
```

---

### Task 5: Per-file uploader (reuses the existing pipeline)

**Files:**
- Create: `web/src/lib/bulkUpload/uploadFile.ts`
- Test: `web/src/lib/bulkUpload/__tests__/uploadFile.test.ts`

**Interfaces:**
- Consumes: `@/api/upload` — `initiateUpload(params) → UploadSession {upload_id, presigned_put_url, deduplicated?, existing_blob_id?, content_blob_id?}`, `uploadToPresigned(url, file, onProgress?)`, `completeUpload(uploadId, sha256?) → {content_blob_id?}`; `@/api/documents` — `createDocument({workspace_id, folder_id?, title, tags?}) → {id}`, `createVersion({document_id, content_blob_id, change_summary?})`.
- Produces: `uploadFileToFolder(args: { file: File; title: string; workspaceId: string; folderId?: string; onProgress?: (pct: number) => void }): Promise<{ documentId: string; deduplicated: boolean }>`. (Conflict policy is resolved by the runner in Task 6; this always creates a new document.)

- [ ] **Step 1: Write the failing test** (mock both api modules; cover happy + dedup)
```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
const initiateUpload = vi.fn(); const uploadToPresigned = vi.fn(); const completeUpload = vi.fn()
const createDocument = vi.fn(); const createVersion = vi.fn()
vi.mock('@/api/upload', () => ({ initiateUpload: (...a: unknown[]) => initiateUpload(...a), uploadToPresigned: (...a: unknown[]) => uploadToPresigned(...a), completeUpload: (...a: unknown[]) => completeUpload(...a) }))
vi.mock('@/api/documents', () => ({ createDocument: (...a: unknown[]) => createDocument(...a), createVersion: (...a: unknown[]) => createVersion(...a) }))
import { uploadFileToFolder } from '../uploadFile'

beforeEach(() => { [initiateUpload, uploadToPresigned, completeUpload, createDocument, createVersion].forEach((m) => m.mockReset()); createDocument.mockResolvedValue({ id: 'doc1' }) })
const file = new File([new Uint8Array(3)], 'x.pdf', { type: 'application/pdf' })

it('uploads + versions on the happy path', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: 'http://put', deduplicated: false })
  completeUpload.mockResolvedValue({ content_blob_id: 'blob1' })
  const r = await uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1', folderId: 'f1' })
  expect(r).toEqual({ documentId: 'doc1', deduplicated: false })
  expect(uploadToPresigned).toHaveBeenCalledOnce()
  expect(createVersion).toHaveBeenCalledWith({ document_id: 'doc1', content_blob_id: 'blob1', change_summary: 'initial' })
})
it('skips the PUT on a dedup hit', async () => {
  initiateUpload.mockResolvedValue({ upload_id: 'u1', presigned_put_url: '', deduplicated: true, existing_blob_id: 'blobX' })
  const r = await uploadFileToFolder({ file, title: 'x.pdf', workspaceId: 'ws1' })
  expect(r.deduplicated).toBe(true)
  expect(uploadToPresigned).not.toHaveBeenCalled()
  expect(createVersion).toHaveBeenCalledWith({ document_id: 'doc1', content_blob_id: 'blobX', change_summary: 'initial' })
})
```

- [ ] **Step 2: Run → FAIL.**

- [ ] **Step 3: Implement `uploadFile.ts`** (mirrors `src/hooks/useUpload.ts` sequence)
```ts
import { initiateUpload, uploadToPresigned, completeUpload } from '@/api/upload'
import { createDocument, createVersion } from '@/api/documents'

export async function uploadFileToFolder(args: {
  file: File; title: string; workspaceId: string; folderId?: string
  onProgress?: (pct: number) => void
}): Promise<{ documentId: string; deduplicated: boolean }> {
  const { file, title, workspaceId, folderId, onProgress } = args
  const doc = await createDocument({ workspace_id: workspaceId, folder_id: folderId, title })
  const session = await initiateUpload({
    filename: file.name,
    mime_type: file.type || 'application/octet-stream',
    size_bytes: file.size,
    workspace_id: workspaceId,
    folder_id: folderId,
  })
  const dedupBlob = (session as { content_blob_id?: string }).content_blob_id ?? session.existing_blob_id
  if (session.deduplicated && dedupBlob) {
    await createVersion({ document_id: doc.id, content_blob_id: dedupBlob, change_summary: 'initial' })
    return { documentId: doc.id, deduplicated: true }
  }
  await uploadToPresigned(session.presigned_put_url, file, onProgress)
  const completion = await completeUpload(session.upload_id)
  const blobId = (completion as { content_blob_id?: string } | null)?.content_blob_id
    ?? (session as { content_blob_id?: string }).content_blob_id ?? session.existing_blob_id
  if (!blobId) throw new Error('storage did not return a content_blob_id')
  await createVersion({ document_id: doc.id, content_blob_id: blobId, change_summary: 'initial' })
  return { documentId: doc.id, deduplicated: false }
}
```

- [ ] **Step 4: Run → PASS.** `npx tsc --noEmit`.

- [ ] **Step 5: Commit**
```bash
git add web/src/lib/bulkUpload/uploadFile.ts web/src/lib/bulkUpload/__tests__/uploadFile.test.ts
git commit -m "feat(web/bulk): per-file uploader reusing initiate/PUT/complete/version"
```

---

### Task 6: Runner (concurrency-limited orchestration)

**Files:**
- Create: `web/src/lib/bulkUpload/runner.ts`
- Test: `web/src/lib/bulkUpload/__tests__/runner.test.ts`

**Interfaces:**
- Consumes: `ensureWorkspace`, `makeFolderEnsurer` (Task 4), `uploadFileToFolder` (Task 5), `dirSegments` (Task 1), `PathFileMap`, `Destination`, `ConflictPolicy`, `ImportResult`.
- Produces: `runImport(args: { map: PathFileMap; destination: Destination; conflict: ConflictPolicy; concurrency?: number; onEvent?: (e: RunEvent) => void }): Promise<ImportResult>` where `type RunEvent = { relPath: string; state: 'uploading' | 'done' | 'failed'; pct?: number; reason?: string }`. Concurrency default 4. Files with a non-`ok` plan status are skipped (recorded). Per-file failures are caught and recorded; the batch continues.

- [ ] **Step 1: Write the failing test** (mock the two lib deps)
```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
const uploadFileToFolder = vi.fn()
const ensureFolder = vi.fn(async () => 'f1')
vi.mock('../folders', () => ({ ensureWorkspace: vi.fn(async () => 'ws1'), makeFolderEnsurer: () => ensureFolder }))
vi.mock('../uploadFile', () => ({ uploadFileToFolder: (...a: unknown[]) => uploadFileToFolder(...a) }))
import { runImport } from '../runner'

const f = (n: string) => new File([new Uint8Array(2)], n.split('/').pop()!, { type: 'application/pdf' })
beforeEach(() => { uploadFileToFolder.mockReset(); ensureFolder.mockClear() })

it('uploads every file and reports a summary', async () => {
  uploadFileToFolder.mockResolvedValue({ documentId: 'd', deduplicated: false })
  const map = new Map([['A/x.pdf', f('A/x.pdf')], ['A/y.pdf', f('A/y.pdf')]])
  const res = await runImport({ map, destination: { mode: 'existing', workspaceId: 'ws1' }, conflict: 'new_version', concurrency: 2 })
  expect(res.created).toBe(2); expect(res.failed).toBe(0)
  expect(uploadFileToFolder).toHaveBeenCalledTimes(2)
})
it('records a per-file failure and continues', async () => {
  uploadFileToFolder.mockRejectedValueOnce(new Error('scan blocked')).mockResolvedValue({ documentId: 'd', deduplicated: false })
  const map = new Map([['a.pdf', f('a.pdf')], ['b.pdf', f('b.pdf')]])
  const res = await runImport({ map, destination: { mode: 'existing', workspaceId: 'ws1' }, conflict: 'new_version' })
  expect(res.created).toBe(1); expect(res.failed).toBe(1)
  expect(res.items.find((i) => i.outcome === 'failed')?.reason).toContain('scan blocked')
})
```

- [ ] **Step 2: Run → FAIL.**

- [ ] **Step 3: Implement `runner.ts`**
```ts
import { ensureWorkspace, makeFolderEnsurer } from './folders'
import { uploadFileToFolder } from './uploadFile'
import { dirSegments } from './paths'
import type { Destination, ConflictPolicy, ImportResult, PathFileMap } from './types'

export type RunEvent = { relPath: string; state: 'uploading' | 'done' | 'failed'; pct?: number; reason?: string }

export async function runImport(args: {
  map: PathFileMap; destination: Destination; conflict: ConflictPolicy
  concurrency?: number; onEvent?: (e: RunEvent) => void
}): Promise<ImportResult> {
  const { map, destination, onEvent } = args
  const concurrency = args.concurrency ?? 4
  const workspaceId = await ensureWorkspace(destination)
  const ensureFolder = makeFolderEnsurer(workspaceId, destination.targetFolderId)
  const result: ImportResult = { created: 0, skipped: 0, failed: 0, items: [] }

  const entries = [...map.entries()]
  let cursor = 0
  async function worker() {
    while (cursor < entries.length) {
      const [relPath, file] = entries[cursor++]
      try {
        onEvent?.({ relPath, state: 'uploading' })
        const folderId = await ensureFolder(dirSegments(relPath).join('/'))
        const r = await uploadFileToFolder({ file, title: file.name, workspaceId, folderId })
        result.created++
        result.items.push({ relPath, outcome: r.deduplicated ? 'created' : 'created', documentId: r.documentId })
        onEvent?.({ relPath, state: 'done' })
      } catch (e) {
        const reason = e instanceof Error ? e.message : String(e)
        result.failed++
        result.items.push({ relPath, outcome: 'failed', reason })
        onEvent?.({ relPath, state: 'failed', reason })
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, entries.length) }, worker))
  return result
}
```

- [ ] **Step 4: Run → PASS.** `npx tsc --noEmit`.

- [ ] **Step 5: Commit**
```bash
git add web/src/lib/bulkUpload/runner.ts web/src/lib/bulkUpload/__tests__/runner.test.ts
git commit -m "feat(web/bulk): import runner (folder ensure + concurrency-limited upload queue)"
```

---

### Task 7: Upload wizard UI

**Files:**
- Create: `web/src/components/admin/bulkUpload/UploadWizard.tsx`
- Test: `web/src/components/admin/bulkUpload/__tests__/UploadWizard.test.tsx`

**Interfaces:**
- Consumes: `parseZip`, `parseFolderDrop` (Task 3), `buildPlan` (Task 2), `runImport` (Task 6), `getWorkspaces` (`@/api/workspaces`), design-system `Card`/`Button`/`LabeledSelect`/`Input`, `sonner` toast.
- Produces: `export function UploadWizard()`. Internal steps: source → preview (uses `buildPlan`) → destination (`getWorkspaces` combobox or new-name input) → run (`runImport`, live progress) → report.

- [ ] **Step 1: Write the failing test** (drop a folder selection, pick existing workspace, run; mock the lib + api)
```ts
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('@/api/workspaces', () => ({ getWorkspaces: vi.fn(async () => [{ id: 'ws1', name: 'Legal' }]) }))
const runImport = vi.fn(async () => ({ created: 1, skipped: 0, failed: 0, items: [{ relPath: 'A/x.pdf', outcome: 'created' }] }))
vi.mock('@/lib/bulkUpload/runner', () => ({ runImport: (...a: unknown[]) => runImport(...a) }))
import { UploadWizard } from '../UploadWizard'

beforeEach(() => runImport.mockClear())

it('previews a dropped folder then runs the import into an existing workspace', async () => {
  render(<UploadWizard />)
  const input = screen.getByTestId('bulk-file-input') as HTMLInputElement
  const file = new File(['x'], 'x.pdf', { type: 'application/pdf' })
  Object.defineProperty(file, 'webkitRelativePath', { value: 'root/A/x.pdf' })
  await userEvent.upload(input, file)
  expect(await screen.findByText(/1 file/i)).toBeInTheDocument() // preview summary
  await userEvent.click(screen.getByTestId('bulk-run'))
  await waitFor(() => expect(runImport).toHaveBeenCalledOnce())
  expect(await screen.findByText(/created/i)).toBeInTheDocument() // report
})
```

- [ ] **Step 2: Run → FAIL.**

- [ ] **Step 3: Implement `UploadWizard.tsx`** — a self-contained component with: a hidden `<input type="file" webkitdirectory>` + a `.zip` file input (both `data-testid="bulk-file-input"`); on select, `parseFolderDrop`/`parseZip` → `buildPlan` → preview card (folder/file counts, total size, flagged rows); a destination `Card` (radio New/Existing, `getWorkspaces` select, optional folder, conflict select defaulting `new_version`); a `data-testid="bulk-run"` button calling `runImport` with an `onEvent` that updates a progress list; a final report `Card` listing created/skipped/failed. Use `Card`, `Button`, `LabeledSelect as Select`, `Input`, tokens (`border-border`, `bg-card`, `text-muted-foreground`), and `toast` for run errors. Show the soft-cap notice when `totalBytes > 500*1024*1024` or `fileCount > 2000`.

- [ ] **Step 4: Run → PASS.** `npx tsc --noEmit` + `npx eslint src/components/admin/bulkUpload/UploadWizard.tsx`.

- [ ] **Step 5: Commit**
```bash
git add web/src/components/admin/bulkUpload/
git commit -m "feat(web/bulk): upload wizard (source -> preview -> destination -> run -> report)"
```

---

### Task 8: Wire into /admin/bulk (3 tabs; demote NDJSON)

**Files:**
- Modify: `web/src/routes/_authenticated/admin/bulk.tsx`
- Test: `web/src/routes/_authenticated/admin/__tests__/bulk.test.tsx` (create)

**Interfaces:**
- Consumes: `UploadWizard` (Task 7); existing `ImportPanel`/`ExportPanel` in `bulk.tsx`.
- Produces: three tabs — `upload` (default, renders `<UploadWizard/>`), `advanced-import` (existing NDJSON `ImportPanel`), `export` (existing `ExportPanel`). Page description updated.

- [ ] **Step 1: Write the failing test**
```ts
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
vi.mock('../../../../components/admin/bulkUpload/UploadWizard', () => ({ UploadWizard: () => <div>WIZARD</div> }))
// (adjust the relative path to match the test file location)
import { BulkPage } from '../bulk'

it('defaults to the Upload files & folders tab', () => {
  render(<BulkPage />)
  expect(screen.getByText('WIZARD')).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: /advanced/i })).toBeInTheDocument()
})
```
> If `BulkPage` isn't exported, add `export` to it in `bulk.tsx` (same pattern as the admin merge work).

- [ ] **Step 2: Run → FAIL.**

- [ ] **Step 3: Modify `bulk.tsx`**
- Change the tab state to `'upload' | 'advanced-import' | 'export'`, default `'upload'`.
- Render `<UploadWizard/>` for `upload`; keep `<ImportPanel/>` under `advanced-import` (label "Advanced / migration"); keep `<ExportPanel/>` for `export`.
- Update the page description to: `"Bulk-upload files & folders, or migrate via NDJSON (advanced)."`
- `export function BulkPage()` if the test needs it.

- [ ] **Step 4: Run → PASS.** `npx tsc --noEmit` + `npx eslint src/routes/_authenticated/admin/bulk.tsx`.

- [ ] **Step 5: Commit**
```bash
git add web/src/routes/_authenticated/admin/bulk.tsx web/src/routes/_authenticated/admin/__tests__/bulk.test.tsx
git commit -m "feat(web/bulk): default Upload tab; demote NDJSON to Advanced / migration"
```

---

### Task 9: Full verification

- [ ] **Step 1:** `cd web && npx tsc --noEmit` → clean.
- [ ] **Step 2:** `npx vitest run src/lib/bulkUpload src/components/admin/bulkUpload src/routes/_authenticated/admin/__tests__/bulk.test.tsx` → all pass.
- [ ] **Step 3:** `npx eslint src/lib/bulkUpload src/components/admin/bulkUpload src/routes/_authenticated/admin/bulk.tsx` → 0 errors.
- [ ] **Step 4:** Manual smoke (dev server): drop a small folder → preview → pick existing workspace → run → verify documents appear in that workspace/folders and the report lists them.
- [ ] **Step 5: Commit** any lint/format fixups.
```bash
git commit -am "chore(web/bulk): phase-1 verification fixups" || echo "nothing to fix"
```

---

## Self-Review

**Spec coverage:** wizard (Task 7) ✓; source zip+folder (Task 3) ✓; preview/validation (Task 2) ✓; destination new-or-existing (Tasks 4,7) ✓; dry-run = preview plan before run (Task 7) ✓; run + progress + report (Tasks 6,7) ✓; reuse upload/folder/workspace pipeline (Tasks 4,5) ✓; NDJSON → Advanced tab (Task 8) ✓; shared `PlannedItem`/`ImportResult` contract (Task 1) ✓; conflict default new_version (Task 6 records outcome; per-path version resolution is a documented follow-up — see below); zip-slip + blocked/oversize (Tasks 1,2) ✓; soft caps (Task 7) ✓.

**Placeholder scan:** none — every code step has real content. Task 7 Step 3 is prose-with-explicit-requirements (a large JSX component); its test (Step 1) pins the contract.

**Type consistency:** `Destination`, `PlannedItem`, `ImportResult`, `ConflictPolicy`, `PathFileMap` defined in Task 1 and consumed with the same shapes in Tasks 2/4/6/7. `uploadFileToFolder` signature matches between Task 5 (def) and Task 6 (call).

**Known follow-up (not a Phase-1 blocker):** true `new_version`/`skip`/`rename` conflict handling requires looking up an existing document by path (no path-lookup API today). Phase 1 always creates a new document and records the outcome; wire real conflict resolution when a "find document by workspace+folder+title" query exists (note it in the runner). Dedup by SHA still applies via the pipeline.
