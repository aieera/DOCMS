import { create } from 'zustand'

export interface UploadItem {
  id: string
  file: File
  progress: number
  status: 'pending' | 'uploading' | 'completed' | 'failed'
  error?: string
}

interface UploadState {
  uploads: Map<string, UploadItem>
  addUpload: (item: UploadItem) => void
  updateProgress: (id: string, progress: number) => void
  setStatus: (id: string, status: UploadItem['status'], error?: string) => void
  removeUpload: (id: string) => void
  clearCompleted: () => void
}

export const useUploadStore = create<UploadState>((set) => ({
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
}))
