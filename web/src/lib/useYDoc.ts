// useYDoc — React hook that opens a Yjs document over the collaboration
// service WebSocket (services/collaboration/src/yjs-server.js).
//
// Per ADR 0096 (§17.4):
//   - Per-(tenant, doc) room — URL path is /yjs/{tenantId}/{docId}.
//   - Reconnection + exponential backoff is built into y-websocket's
//     WebsocketProvider (initial 1 s, max ~30 s with jitter).
//   - Close codes (CB-3, collaboration yjs-server.js):
//       4400 invalid room id        → stop, status 'invalid_room', toast
//       4401 no / expired session   → stop, status 'auth_failed'
//       4403 tenant mismatch, or view revoked mid-session (re-checked
//            every 60 s)            → stop, status 'access_revoked'
//       4503 auth/document service unreachable, 1012 server restart
//                                   → y-websocket's backoff reconnect
//     Spinning on a permanent failure is wasted work.
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

import { toast } from 'sonner'

import { useAuthStore } from '@/store/authStore'

export type YDocStatus =
  | 'connecting'
  | 'connected'
  | 'disconnected'
  | 'auth_failed'
  | 'access_revoked'
  | 'invalid_room'

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
      const terminal: YDocStatus | null =
        code === 4400 ? 'invalid_room'
          : code === 4401 ? 'auth_failed'
            : code === 4403 ? 'access_revoked'
              : null
      if (terminal) {
        provider.shouldConnect = false
        if (terminal === 'invalid_room') toast.error('Live editing is unavailable for this document.')
        setHandle((prev) => (prev ? { ...prev, status: terminal } : prev))
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
