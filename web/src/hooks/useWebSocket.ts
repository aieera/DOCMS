import { useEffect, useRef, useCallback } from 'react'
import { useAuthStore } from '@/store/authStore'

export function useWebSocket(onMessage: (data: unknown) => void) {
  const ws = useRef<WebSocket | null>(null)
  const reconnectDelay = useRef(1000)
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)

  const connect = useCallback(() => {
    if (!isAuthenticated) return
    // The HttpOnly session cookie is sent automatically by the browser
    // during the WebSocket handshake (same-origin). The server reads it
    // from the Upgrade request's Cookie header — no token in the URL.
    const url = `${window.location.protocol === 'https:' ? 'wss' : 'ws'}://${window.location.host}/ws`
    const socket = new WebSocket(url)
    ws.current = socket
    socket.onopen = () => { reconnectDelay.current = 1000 }
    socket.onmessage = (e) => { try { onMessage(JSON.parse(e.data)) } catch {} }
    socket.onclose = () => {
      setTimeout(connect, reconnectDelay.current)
      reconnectDelay.current = Math.min(reconnectDelay.current * 2, 30_000)
    }
  }, [isAuthenticated, onMessage])

  useEffect(() => { connect(); return () => { ws.current?.close() } }, [connect])

  return { send: (data: unknown) => ws.current?.send(JSON.stringify(data)) }
}
