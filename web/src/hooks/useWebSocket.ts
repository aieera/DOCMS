import { useEffect, useRef, useCallback } from 'react'
import { useAuthStore } from '@/store/authStore'

// useWebSocket subscribes to real-time updates via the platform's
// /ws endpoint. The HttpOnly session cookie is sent automatically
// during the Upgrade handshake (same-origin) — no token in the URL.
//
// Feature-flag: only connects when VITE_ENABLE_WS=1. The /ws
// endpoint isn't wired in every deployment yet (Wave 12 Redis-
// pub/sub fanout is still in progress), and reconnect spam without
// it floods the dev console + runs an exponential-backoff timer
// forever. When the flag is off the hook returns a no-op send().
export function useWebSocket(onMessage: (data: unknown) => void) {
  const ws = useRef<WebSocket | null>(null)
  const reconnectDelay = useRef(1000)
  const giveUp = useRef(false)
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)
  const enabled = (import.meta as { env?: { VITE_ENABLE_WS?: string } }).env?.VITE_ENABLE_WS === '1'

  const connect = useCallback(() => {
    if (!isAuthenticated || !enabled || giveUp.current) return
    const url = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`
    const socket = new WebSocket(url)
    ws.current = socket
    socket.onopen = () => { reconnectDelay.current = 1000 }
    socket.onmessage = (e) => { try { onMessage(JSON.parse(e.data)) } catch { /* drop malformed */ } }
    socket.onerror = () => {
      // First failure → mark dead so we don't backoff-loop in dev
      // when /ws isn't available at all. Real prod retries via
      // onclose's exponential schedule.
      if (reconnectDelay.current >= 8000) giveUp.current = true
    }
    socket.onclose = () => {
      if (giveUp.current) return
      setTimeout(connect, reconnectDelay.current)
      reconnectDelay.current = Math.min(reconnectDelay.current * 2, 30_000)
    }
  }, [isAuthenticated, enabled, onMessage])

  useEffect(() => { connect(); return () => { ws.current?.close() } }, [connect])

  return { send: (data: unknown) => ws.current?.send(JSON.stringify(data)) }
}
