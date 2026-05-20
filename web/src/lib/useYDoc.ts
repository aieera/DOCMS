// useYDoc — React hook that opens a Yjs document over the collaboration
// service WebSocket (services/collaboration/src/yjs-server.js).
//
// Per ADR 0096 (§17.4):
//   - Per-(tenant, doc) room — URL path is /yjs/{tenantId}/{docId}.
//   - Reconnection + exponential backoff is built into y-websocket's
//     WebsocketProvider (initial 1 s, max ~30 s with jitter).
//   - Custom close codes 4401 (auth) / 4403 (tenant mismatch) tell the
//     hook to STOP retrying — spinning on a permanent auth failure is
//     wasted work.
//   - Cookie auth happens at WebSocket Upgrade. The browser sends the
//     dms_session cookie automatically because we hit the same origin
//     (Vite proxies /yjs/* → ws://localhost:8083/yjs/*).
//
// Today's consumer: `web/src/components/document/CommentsRealtime`
// (Phase 1, reference implementation). Annotation layers + presence
// indicators are Phase 3 work in the ADR.
import { useEffect, useState } from 'react'
import * as Y from 'yjs'
import { WebsocketProvider } from 'y-websocket'

import { useAuthStore } from '@/store/authStore'

export type YDocStatus = 'connecting' | 'connected' | 'disconnected' | 'auth_failed'

export interface YDocHandle {
  ydoc: Y.Doc
  provider: WebsocketProvider
  awareness: WebsocketProvider['awareness']
  status: YDocStatus
}

/**
 * Opens a Y.Doc bound to a (tenant, doc) room.
 *
 * Returns null until the tenant is resolved from auth state. After that,
 * the same handle is reused for the lifetime of the component; `status`
 * updates as the underlying socket connects / disconnects.
 *
 * Tear-down: destroys the provider AND the Y.Doc on unmount. Don't keep
 * a long-lived reference to the returned object outside this hook.
 */
export function useYDoc(docId: string | undefined): YDocHandle | null {
  const tenantId = useAuthStore((s) => s.tenantId)
  const [handle, setHandle] = useState<YDocHandle | null>(null)

  useEffect(() => {
    if (!docId || !tenantId) {
      setHandle(null)
      return
    }
    const ydoc = new Y.Doc()
    // Same-origin so dms_session cookie rides along on the upgrade.
    // Vite proxies /yjs/* to ws://localhost:8083/yjs/* (configured in
    // vite.config.ts). In prod, the gateway forwards /yjs/* to the
    // collaboration service.
    const wsScheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const wsUrl = `${wsScheme}://${window.location.host}/yjs/${tenantId}`
    const provider = new WebsocketProvider(wsUrl, docId, ydoc)

    const update = () => {
      // y-websocket exposes a "wsconnected" boolean and a "status"
      // event with payload { status: 'connected' | 'connecting' |
      // 'disconnected' }. Mirror that into our public state.
      setHandle((prev) =>
        prev ? { ...prev, status: providerStatus(provider) } : prev,
      )
    }

    provider.on('status', update)
    provider.on('connection-close', (event: { code?: number } | null | undefined) => {
      // event is null for pre-handshake failures (TCP refused before
      // the upgrade completed). Only inspect when we have a real
      // CloseEvent.
      const code = event?.code
      // Auth failures: stop the reconnect loop. 4401 = no/bad cookie,
      // 4403 = tenant mismatch (caller doesn't belong to the room's
      // tenant). Both are caller-side bugs that won't resolve via retry.
      if (code === 4401 || code === 4403) {
        provider.shouldConnect = false
        setHandle((prev) =>
          prev ? { ...prev, status: 'auth_failed' } : prev,
        )
      }
    })

    setHandle({
      ydoc,
      provider,
      awareness: provider.awareness,
      status: providerStatus(provider),
    })

    return () => {
      provider.off('status', update)
      provider.destroy()
      ydoc.destroy()
    }
  }, [docId, tenantId])

  return handle
}

function providerStatus(p: WebsocketProvider): YDocStatus {
  if (p.wsconnected) return 'connected'
  if (p.wsconnecting) return 'connecting'
  return 'disconnected'
}
