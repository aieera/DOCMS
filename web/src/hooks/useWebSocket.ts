import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import { getCurrentUser } from '@/api/auth'
import { getCollabToken } from '@/lib/collabToken'

// useWebSocket — client for the collaboration service's /ws endpoint
// (comments + presence).
//
// Protocol (services/collaboration src/handler.js + server.js):
//   1. The first frame must be {type:'auth', token}; without it the
//      server closes 4001 after 10 s. The upgrade itself is signed by
//      the gateway / dev proxy (CB-6). Live frames start after
//      {type:'auth_ok'}; only then do we {type:'subscribe', doc_id}.
//   2. Access is re-checked every 60 s (CB-3):
//        - 4001 / 4401 / 4403 — session or access lost: re-validate the
//          session (/auth/me) and reconnect ONCE after a pause; a second
//          auth loss inside the window gives up (no tight loop);
//        - 4503 / 1012 / abnormal closes — exponential backoff reconnect;
//        - 4400 — invalid room id: permanent, no reconnect, one toast;
//        - {type:'subscription_revoked', doc_id} — the doc's live
//          comments/presence stop; it is never resubscribed
//          automatically (not even after a reconnect).
//
// Feature-flag: only connects when VITE_ENABLE_WS=1 (or opts.enabled).

const AUTH_LOSS_CODES = new Set([4001, 4401, 4403])
const PERMANENT_CODES = new Set([4400, 1008])
const BASE_DELAY_MS = 1000
const MAX_DELAY_MS = 30_000
const REAUTH_DELAY_MS = 5_000
const REAUTH_WINDOW_MS = 60_000
const MAX_FAILED_OPENS = 6

export interface UseWebSocketOptions {
  /** Document whose comments/presence to subscribe to after auth_ok. */
  docId?: string
  /** Called once when the server revokes the doc subscription. */
  onRevoked?: (docId: string) => void
  /** Override the feature flag (tests / explicit opt-in). */
  enabled?: boolean
  /** Token source for the auth frame (defaults to the in-memory session token). */
  getToken?: () => string | null
}

type Frame = { type?: string; doc_id?: string }

export function useWebSocket(onMessage: (data: unknown) => void, opts: UseWebSocketOptions = {}) {
  const ws = useRef<WebSocket | null>(null)
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const flag = (import.meta as { env?: { VITE_ENABLE_WS?: string } }).env?.VITE_ENABLE_WS === '1'
  const enabled = opts.enabled ?? flag
  const { docId } = opts
  const [revoked, setRevoked] = useState(false)

  // Latest callbacks without re-running the connection effect.
  const onMessageRef = useRef(onMessage)
  const onRevokedRef = useRef(opts.onRevoked)
  const getTokenRef = useRef(opts.getToken ?? getCollabToken)
  useEffect(() => {
    onMessageRef.current = onMessage
    onRevokedRef.current = opts.onRevoked
    getTokenRef.current = opts.getToken ?? getCollabToken
  })

  useEffect(() => {
    if (!isAuthenticated || !enabled) return
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | null = null
    let delay = BASE_DELAY_MS
    let failedOpens = 0
    let lastReauthAt = -Infinity
    let docRevoked = false

    const schedule = (ms: number) => {
      if (stopped) return
      timer = setTimeout(() => { timer = null; connect() }, ms)
    }

    const connect = () => {
      if (stopped) return
      const token = getTokenRef.current()
      if (!token) return // nothing to authenticate with; stay idle
      const url = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`
      const socket = new WebSocket(url)
      ws.current = socket
      let opened = false

      socket.onopen = () => {
        opened = true
        failedOpens = 0
        socket.send(JSON.stringify({ type: 'auth', token }))
      }
      socket.onmessage = (e) => {
        let msg: Frame
        try { msg = JSON.parse(e.data) as Frame } catch { return }
        if (msg.type === 'auth_ok') {
          delay = BASE_DELAY_MS
          if (docId && !docRevoked) socket.send(JSON.stringify({ type: 'subscribe', doc_id: docId }))
          return
        }
        if (msg.type === 'subscription_revoked') {
          if (msg.doc_id && msg.doc_id === docId && !docRevoked) {
            docRevoked = true
            setRevoked(true)
            onRevokedRef.current?.(msg.doc_id)
          }
          return
        }
        // After revocation, nothing about that document is rendered.
        if (docRevoked && msg.doc_id === docId) return
        onMessageRef.current(msg)
      }
      socket.onclose = (e) => {
        if (ws.current === socket) ws.current = null
        if (stopped) return
        const code = e?.code
        if (code !== undefined && PERMANENT_CODES.has(code)) {
          stopped = true
          toast.error('Live updates are unavailable for this document.')
          return
        }
        if (code !== undefined && AUTH_LOSS_CODES.has(code)) {
          const now = Date.now()
          if (now - lastReauthAt < REAUTH_WINDOW_MS) { stopped = true; return }
          lastReauthAt = now
          // Re-validate the session; a dead session is the 401
          // interceptor's job (logout), not a reason to keep retrying.
          getCurrentUser()
            .then(() => schedule(Math.max(REAUTH_DELAY_MS, delay)))
            .catch(() => { stopped = true })
          return
        }
        if (!opened && ++failedOpens >= MAX_FAILED_OPENS) { stopped = true; return }
        const wait = delay
        delay = Math.min(delay * 2, MAX_DELAY_MS)
        schedule(wait)
      }
    }

    connect()
    return () => {
      stopped = true
      if (timer) clearTimeout(timer)
      ws.current?.close(1000)
      ws.current = null
    }
  }, [isAuthenticated, enabled, docId])

  return {
    send: (data: unknown) => {
      if (ws.current?.readyState === 1) ws.current.send(JSON.stringify(data))
    },
    revoked,
  }
}
