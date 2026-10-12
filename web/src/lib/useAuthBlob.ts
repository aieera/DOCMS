// useAuthBlob — fetches a protected URL via the axios client (same-origin,
// so the HttpOnly session cookie rides along) and returns a blob: URL
// suitable for <img src>, <video src>, etc.
//
// Why this exists: the axios client adds what a native <img>/<video>
// load cannot — the CSRF header, the 401/session handling and error
// toasts — and a blob: URL keeps the authenticated bytes out of the
// browser's shared HTTP cache. Identity itself is the cookie only
// (DEC-3); no identity headers are sent.
import { useEffect, useState } from 'react'

import { api } from '@/api/client'

export function useAuthBlob(url: string | undefined): string | undefined {
  const [blobUrl, setBlobUrl] = useState<string | undefined>(undefined)

  useEffect(() => {
    if (!url) {
      setBlobUrl(undefined)
      return
    }
    let cancelled = false
    let created: string | undefined
    // Callers commonly pass the absolute path `/api/v1/...`. The axios
    // client has baseURL=/api/v1, so we must strip the prefix to avoid
    // requesting `/api/v1/api/v1/...`.
    const relPath = url.startsWith('/api/v1/') ? url.slice('/api/v1'.length) : url
    api
      .get(relPath, { responseType: 'blob' })
      .then((res) => {
        if (cancelled) return
        created = URL.createObjectURL(res.data)
        setBlobUrl(created)
      })
      .catch(() => {
        if (cancelled) return
        setBlobUrl(undefined)
      })
    return () => {
      cancelled = true
      if (created) URL.revokeObjectURL(created)
    }
  }, [url])

  return blobUrl
}
