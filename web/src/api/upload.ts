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

// toProxiedUploadUrl routes the presigned PUT through Vite's same-origin
// `/s3` proxy whenever the presigned URL points at the dev MinIO public
// base (localhost:9000). PUTting there directly is a cross-origin request
// to a second port that silently fails whenever the browser can't reach
// :9000 (remote / port-forwarded dev hosts), leaving a 0-byte document.
// Vite's `/s3` proxy forwards to MinIO with the Host rewritten so the S3
// `SignedHeaders=host` signature still validates. The localhost:9000 host
// only ever appears in dev — prod presigned URLs point at the real S3
// endpoint — so this rewrite is a no-op in production.
function toProxiedUploadUrl(raw: string): string {
  try {
    const u = new URL(raw)
    if (u.port === '9000' && (u.hostname === 'localhost' || u.hostname === '127.0.0.1')) {
      return `/s3${u.pathname}${u.search}`
    }
  } catch {
    // Not an absolute URL — leave it untouched.
  }
  return raw
}

export async function uploadToPresigned(
  url: string, file: File, onProgress?: (pct: number) => void,
) {
  await axios.put(toProxiedUploadUrl(url), file, {
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
