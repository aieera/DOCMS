import { api } from './client'
import axios from 'axios'
import type { UploadSession } from '@/types/api'

export async function initiateUpload(params: {
  filename: string; mime_type: string; size_bytes: number; sha256_hash?: string;
  workspace_id?: string; folder_id?: string;
}) {
  const { data } = await api.post<UploadSession>('/storage/uploads/initiate', params)
  return data
}

export async function uploadToPresigned(
  url: string, file: File, onProgress?: (pct: number) => void,
) {
  await axios.put(url, file, {
    headers: { 'Content-Type': file.type },
    onUploadProgress: (e) => {
      if (e.total && onProgress) onProgress(Math.round((e.loaded * 100) / e.total))
    },
  })
}

export async function completeUpload(uploadId: string, sha256?: string) {
  const { data } = await api.post(`/storage/uploads/${uploadId}/complete`, { sha256_hash: sha256 })
  return data
}
