// RealtimePresence — Yjs awareness-backed "who else is viewing" strip
// (ADR 0096 Phase 1 reference consumer).
//
// Renders an avatar pile of other users currently viewing the same
// (tenant, doc) room, plus an offline indicator if the CRDT WebSocket
// can't reach the collaboration service.
//
// Why presence first, not comments: refactoring the comments list to
// the Yjs path requires a dual-write transition (Phase 3 in the ADR)
// and breaks the existing happy path during the migration. Presence
// is additive — drop it into the doc detail page, exercises the full
// useYDoc + awareness + WS auth + snapshot persistence chain, and
// proves the foundation works without risking the comments flow.
import { useEffect, useState } from 'react'
import { Wifi, WifiOff } from 'lucide-react'

import { useYDoc } from '@/lib/useYDoc'
import { useAuthStore } from '@/store/authStore'

interface PresenceState {
  user: { id: string; name: string; email: string }
  cursor?: { x: number; y: number }
  color?: string
}

export function RealtimePresence({ documentId }: { documentId: string }) {
  const handle = useYDoc(documentId)
  const me = useAuthStore((s) => s.user)
  const [peers, setPeers] = useState<PresenceState[]>([])

  // Publish our identity to the awareness map on connect.
  useEffect(() => {
    if (!handle || !me) return
    const localUser: PresenceState['user'] = {
      id: me.id,
      name: me.display_name || me.email,
      email: me.email,
    }
    handle.awareness.setLocalStateField('user', localUser)
    handle.awareness.setLocalStateField('color', colorForUser(me.id))
    return () => {
      handle.awareness.setLocalStateField('user', null)
    }
  }, [handle, me])

  // Subscribe to remote state changes. Filter out our own entry.
  useEffect(() => {
    if (!handle || !me) return
    const update = () => {
      const states: PresenceState[] = []
      handle.awareness.getStates().forEach((s, clientID) => {
        if (clientID === handle.awareness.clientID) return
        if (!s.user) return
        states.push(s as PresenceState)
      })
      setPeers(states)
    }
    update()
    handle.awareness.on('change', update)
    return () => handle.awareness.off('change', update)
  }, [handle, me])

  if (!handle) return null

  return (
    <div className="flex items-center gap-2" data-testid="realtime-presence">
      <ConnectionDot status={handle.status} />
      {peers.length === 0 ? (
        <span className="text-xs text-muted-foreground">No one else here</span>
      ) : (
        <div className="flex -space-x-2">
          {peers.slice(0, 5).map((p, i) => (
            <Avatar key={p.user.id + i} name={p.user.name} color={p.color} />
          ))}
          {peers.length > 5 && (
            <span className="ms-2 self-center text-xs text-muted-foreground">
              +{peers.length - 5} more
            </span>
          )}
        </div>
      )}
    </div>
  )
}

function ConnectionDot({ status }: { status: 'connecting' | 'connected' | 'disconnected' | 'auth_failed' }) {
  if (status === 'connected') return <Wifi className="h-3.5 w-3.5 text-success" aria-label="Connected" />
  if (status === 'auth_failed') return <WifiOff className="h-3.5 w-3.5 text-red-500" aria-label="Auth failed" />
  return <WifiOff className="h-3.5 w-3.5 text-muted-foreground" aria-label={status} />
}

function Avatar({ name, color }: { name: string; color?: string }) {
  const initials = name
    .split(/\s+/)
    .map((w) => w[0])
    .filter(Boolean)
    .slice(0, 2)
    .join('')
    .toUpperCase()
  return (
    <span
      className="flex h-7 w-7 items-center justify-center rounded-full border-2 border-background text-xs font-medium text-white"
      style={{ backgroundColor: color || '#6b7280' }}
      title={name}
    >
      {initials || '?'}
    </span>
  )
}

// Deterministic color per user — a small palette keyed off a string
// hash of the user id. Awareness updates carry this so every peer sees
// the same color for the same user without server coordination.
function colorForUser(id: string): string {
  const palette = ['#3b82f6', '#10b981', '#f97316', '#8b5cf6', '#ec4899', '#06b6d4', '#eab308']
  let h = 0
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0
  return palette[h % palette.length]
}
