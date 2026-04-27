// CollabPresence — small overlapping-avatars indicator that surfaces
// who else is viewing a document right now. Drives off the
// useCollabPresence hook (WebSocket presence stream).
//
// Render position: alongside the doc title in the detail page header.
// When disconnected (collab service down, no peers), renders nothing —
// no "you are alone" tooltip noise.

import { useCollabPresence, type CollabPeer } from '@/hooks/useCollabPresence'
import { Avatar } from '@/components/ui/Avatar'
import { Wifi, WifiOff } from 'lucide-react'

interface Props {
  documentID: string | undefined
  // Show a small connection dot even when no peers — useful on the
  // doc detail page so the user can tell "I'm connected to the
  // collab hub" vs "I'm alone here".
  showConnectionState?: boolean
}

export function CollabPresence({ documentID, showConnectionState }: Props) {
  const { connected, peers } = useCollabPresence(documentID)

  if (peers.length === 0) {
    if (!showConnectionState) return null
    return (
      <span
        className={`inline-flex items-center gap-1 text-[10px] ${
          connected
            ? 'text-emerald-700 dark:text-emerald-300'
            : 'text-[var(--color-text-secondary)]'
        }`}
        title={connected ? 'Connected to collaboration hub' : 'Collab hub disconnected'}
        data-testid="collab-presence-empty"
      >
        {connected ? <Wifi className="h-3 w-3" /> : <WifiOff className="h-3 w-3" />}
        {connected ? 'live' : 'offline'}
      </span>
    )
  }

  // Show up to 4 avatars overlapping; if more, show a "+N" pill.
  const VISIBLE = 4
  const visible = peers.slice(0, VISIBLE)
  const hidden = peers.length - visible.length

  return (
    <span
      className="inline-flex items-center gap-1"
      data-testid="collab-presence"
      title={`${peers.length} viewer${peers.length === 1 ? '' : 's'} live: ${peers.map((p) => p.display_name).join(', ')}`}
    >
      <span className="flex -space-x-1.5">
        {visible.map((p) => (
          <PeerDot key={p.user_id} peer={p} />
        ))}
      </span>
      {hidden > 0 && (
        <span className="rounded-full bg-[var(--color-bg-secondary)] px-1.5 py-0.5 text-[10px] text-[var(--color-text-secondary)]">
          +{hidden}
        </span>
      )}
    </span>
  )
}

function PeerDot({ peer }: { peer: CollabPeer }) {
  return (
    <span
      className="ring-2 ring-[var(--color-bg)]"
      style={{ borderRadius: '9999px' }}
      data-testid={`collab-peer-${peer.user_id}`}
    >
      <Avatar name={peer.display_name} size="sm" src={peer.avatar_url} />
    </span>
  )
}
