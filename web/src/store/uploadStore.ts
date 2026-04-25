import { create } from 'zustand'

export interface UploadItem {
  id: string
  file: File
  progress: number
  // 'scanning' = PUT finished, backend returned 202-equivalent, we're polling
  //              /storage/uploads/:id/complete. The UploadProgress row shows
  //              a spinner past 10 s and a hard timeout at 120 s.
  // 'mime_mismatch' = finalize rejected the upload because the magic-byte
  //              MIME didn't match the declared Content-Type. Error row
  //              links to the help article.
  status: 'pending' | 'uploading' | 'scanning' | 'completed' | 'failed' | 'mime_mismatch'
  error?: string
  scanStartedAt?: number // ms epoch; drives the 10 s / 120 s timers in UploadProgress
}

interface UploadState {
  uploads: Map<string, UploadItem>
  addUpload: (item: UploadItem) => void
  updateProgress: (id: string, progress: number) => void
  setStatus: (id: string, status: UploadItem['status'], error?: string) => void
  markScanning: (id: string) => void
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
  markScanning: (id) =>
    set((s) => {
      const m = new Map(s.uploads)
      const item = m.get(id)
      if (item) m.set(id, { ...item, status: 'scanning', scanStartedAt: Date.now() })
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
