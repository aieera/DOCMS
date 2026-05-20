// useAuthBlob — fetches a protected URL via the axios client (which
// attaches the session cookie + X-Tenant-ID header) and returns a
// blob: URL suitable for <img src>, <video src>, etc.
//
// Why this exists: browser-native loaders like `<img src="...">`
// cannot attach custom headers — only cookies travel with them. Our
// document download endpoint expects X-Tenant-ID, which axios sets
// for AJAX calls but the browser can't set for an image load. So the
// `<img>` request 401's even though the user is logged in. This hook
// closes the gap by fetching the bytes ourselves and handing back a
// blob: URL the `<img>` element can render unauthenticated.
//
// The backend (ADR 0095 / TenantHTTP) now also accepts cookie-only
// auth via SessionAuthOptional, so this hook is belt-and-braces —
// it works regardless of which side ships first.
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
