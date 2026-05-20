// Sender activity panel for a zero-trust share — ADR 0098 §18 F2.
// Surfaces the telemetry stream the recipient viewer POSTs back.
import { useQuery } from '@tanstack/react-query'
import { formatDistanceToNow } from 'date-fns'
import { Eye, MousePointer, EyeOff, AlertTriangle, X } from 'lucide-react'

import { getZTTelemetry, revokeZTShare, type ZTTelemetryEvent } from '@/api/ztShare'
import { Button } from '@/components/ui/shadcn/button'

export function ZTShareActivity({ tokenId, onRevoked }: { tokenId: string; onRevoked?: () => void }) {
  const { data, isLoading, refetch } = useQuery({
    queryKey: ['zt-telemetry', tokenId],
    queryFn: () => getZTTelemetry(tokenId, 100),
    refetchInterval: 10_000,
  })

  return (
    <section className="rounded-lg border border-border bg-card">
      <header className="flex items-center justify-between border-b border-border px-4 py-2">
        <h3 className="text-sm font-semibold">Recipient activity</h3>
        <Button
          variant="ghost"
          size="sm"
          onClick={async () => {
            if (!confirm('Revoke this share? The recipient loses access immediately.')) return
            await revokeZTShare(tokenId)
            onRevoked?.()
            refetch()
          }}
        >
          <X className="me-1 h-3.5 w-3.5" /> Revoke
        </Button>
      </header>
      <ul className="max-h-96 divide-y divide-border overflow-y-auto">
        {isLoading && <li className="px-4 py-3 text-sm text-muted-foreground">Loading…</li>}
        {data && data.length === 0 && (
          <li className="px-4 py-3 text-sm text-muted-foreground">No views yet.</li>
        )}
        {data?.map((e, i) => (
          <li key={i} className="flex items-center gap-3 px-4 py-2 text-sm">
            <EventIcon type={e.event_type} />
            <span className="flex-1">{describe(e)}</span>
            <span className="text-xs text-muted-foreground">
              {formatDistanceToNow(new Date(e.created_at), { addSuffix: true })}
            </span>
          </li>
        ))}
      </ul>
    </section>
  )
}

function EventIcon({ type }: { type: ZTTelemetryEvent['event_type'] }) {
  switch (type) {
    case 'page_view':     return <Eye className="h-4 w-4 text-emerald-500" />
    case 'scroll':        return <MousePointer className="h-4 w-4 text-blue-500" />
    case 'focus_blur':    return <EyeOff className="h-4 w-4 text-muted-foreground" />
    case 'devtools_open': return <AlertTriangle className="h-4 w-4 text-amber-500" />
  }
}

function describe(e: ZTTelemetryEvent): string {
  switch (e.event_type) {
    case 'page_view': {
      const page = e.page_number ? `page ${e.page_number}` : 'a page'
      const dwell = e.dwell_ms ? ` (${(e.dwell_ms / 1000).toFixed(1)} s dwell)` : ''
      return `Viewed ${page}${dwell}`
    }
    case 'scroll':        return 'Scrolled'
    case 'focus_blur':    return 'Left the viewer tab'
    case 'devtools_open': return 'DevTools opened (heuristic)'
  }
}
