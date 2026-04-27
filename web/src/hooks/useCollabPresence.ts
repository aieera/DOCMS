// GAP-8 — Live collaboration presence hook.
//
// Opens a WebSocket to /ws/collab/{document_id} and tracks who's
// viewing the document right now. The collaboration service
// (services/collaboration/) authenticates on the WS upgrade via
// the dms_session cookie and emits peer_join / peer_leave events
// per its README.
//
// Minimal scope: presence only (the "who is here" badge on the
// document detail page). Cursor tracking + typing indicators
// would mount additional message handlers — kept out of scope
// for this hook so consumers that only need presence don't pay
// the cost.

import { useEffect, useRef, useState } from 'react'

export interface CollabPeer {
  user_id: string
  display_name: string
  avatar_url?: string
  joined_at: string
}

interface JoinMsg { type: 'peer_join'; peer: CollabPeer }
interface LeaveMsg { type: 'peer_leave'; user_id: string }
interface PresenceSnapshotMsg { type: 'presence_snapshot'; peers: CollabPeer[] }

type ServerMessage = JoinMsg | LeaveMsg | PresenceSnapshotMsg | { type: string }

interface State {
  connected: boolean
  peers: Record<string, CollabPeer>
}

export interface UseCollabPresenceResult {
  connected: boolean
  peers: CollabPeer[]
}

// useCollabPresence wraps the WebSocket lifecycle. Re-opens on
// document_id change; closes on unmount. Reconnect-on-disconnect
// is intentionally NOT implemented in this first cut — flaky
// connections show as "disconnected" until the user navigates
// or refreshes. The underlying WebSocket sends server-driven
// pings so the connection stays warm under normal conditions.
export function useCollabPresence(documentID: string | undefined): UseCollabPresenceResult {
  const [state, setState] = useState<State>({ connected: false, peers: {} })
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (!documentID) return
    // Same-origin WS — the dms_session cookie is included automatically.
    // Vite proxy forwards /ws/collab → ws://localhost:8083 in dev.
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${window.location.host}/ws/collab/${encodeURIComponent(documentID)}`

    let cancelled = false
    let socket: WebSocket
    try {
      socket = new WebSocket(url)
    } catch {
      // URL malformed or browser refused; render as disconnected.
      setState({ connected: false, peers: {} })
      return
    }
    wsRef.current = socket

    socket.onopen = () => {
      if (cancelled) return
      setState((s) => ({ ...s, connected: true }))
    }

    socket.onmessage = (ev) => {
      if (cancelled) return
      let msg: ServerMessage
      try {
        msg = JSON.parse(ev.data)
      } catch {
        return
      }
      setState((s) => {
        switch (msg.type) {
          case 'presence_snapshot': {
            const peers: Record<string, CollabPeer> = {}
            for (const p of (msg as PresenceSnapshotMsg).peers ?? []) {
              peers[p.user_id] = p
            }
            return { ...s, peers }
          }
          case 'peer_join': {
            const p = (msg as JoinMsg).peer
            if (!p?.user_id) return s
            return { ...s, peers: { ...s.peers, [p.user_id]: p } }
          }
          case 'peer_leave': {
            const id = (msg as LeaveMsg).user_id
            if (!id) return s
            const next = { ...s.peers }
            delete next[id]
            return { ...s, peers: next }
          }
          default:
            return s
        }
      })
    }

    socket.onclose = () => {
      if (cancelled) return
      setState({ connected: false, peers: {} })
    }
    socket.onerror = () => {
      // onclose will follow; rely on it for cleanup so we don't
      // double-set connected=false.
    }

    return () => {
      cancelled = true
      try {
        socket.close(1000, 'unmount')
      } catch {
        // browser-side close errors aren't actionable
      }
      wsRef.current = null
    }
  }, [documentID])

  return {
    connected: state.connected,
    peers: Object.values(state.peers),
  }
}
