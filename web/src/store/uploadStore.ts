import { create } from 'zustand'
import type { DuplicateMatch } from '@/api/documents'

export interface UploadItem {
  id: string
  file: File
  progress: number
  status: 'pending' | 'uploading' | 'completed' | 'failed'
  error?: string
}

// DuplicatePrompt is an in-flight "this file already exists — upload
// anyway?" decision. uploadFiles awaits the resolver; a globally-mounted
// dialog renders the prompt and resolves it. Uploads are sequential so
// at most one prompt is pending at a time.
export interface DuplicatePrompt {
  fileName: string
  matches: DuplicateMatch[]
  resolve: (proceed: boolean) => void
}

interface UploadState {
  uploads: Map<string, UploadItem>
  addUpload: (item: UploadItem) => void
  updateProgress: (id: string, progress: number) => void
  setStatus: (id: string, status: UploadItem['status'], error?: string) => void
  removeUpload: (id: string) => void
  clearCompleted: () => void
  // Duplicate-confirmation handshake.
  duplicatePrompt: DuplicatePrompt | null
  requestDuplicateDecision: (fileName: string, matches: DuplicateMatch[]) => Promise<boolean>
  resolveDuplicate: (proceed: boolean) => void
}

export const useUploadStore = create<UploadState>((set, get) => ({
  uploads: new Map(),
  addUpload: (item) =>
    set((s) => { const m = new Map(s.uploads); m.set(item.id, item); return { uploads: m } }),
  updateProgress: (id, progress) =>
    set((s) => {
      const m = new Map(s.uploads)
      const item = m.get(id)
      if (item) m.set(id, { ...item, progress })
      return { uploads: m }
    }),
  setStatus: (id, status, error) =>
    set((s) => {
      const m = new Map(s.uploads)
      const item = m.get(id)
      if (item) m.set(id, { ...item, status, error })
      return { uploads: m }
    }),
  removeUpload: (id) =>
    set((s) => { const m = new Map(s.uploads); m.delete(id); return { uploads: m } }),
  clearCompleted: () =>
    set((s) => {
      const m = new Map(s.uploads)
      for (const [k, v] of m) { if (v.status === 'completed') m.delete(k) }
      return { uploads: m }
    }),
  duplicatePrompt: null,
  requestDuplicateDecision: (fileName, matches) =>
    new Promise<boolean>((resolve) => {
      set({ duplicatePrompt: { fileName, matches, resolve } })
    }),
  resolveDuplicate: (proceed) => {
    const cur = get().duplicatePrompt
    cur?.resolve(proceed)
    set({ duplicatePrompt: null })
  },
}))
