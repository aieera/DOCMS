// AuditVisualization — three panels from one API call (ADR 0103 §18 F10).
//
// No new deps: sparkline + bars + heatmap are plain SVG.
// Filter chips drop activity scope to a single actor or action.
import { useMemo, useState } from 'react'
import { formatDateTime } from '@/lib/formatters'
import { useQuery } from '@tanstack/react-query'
import { BarChart3, Users, Activity, Filter, X as XIcon } from 'lucide-react'

import {
  getAuditViz,
  type AuditVizActor,
  type AuditVizAction,
  type AuditVizHeatmapCell,
  type AuditVizSankeyEdge,
  type AuditVizTimeBucket,
} from '@/api/auditViz'

interface Props {
  documentId: string
}

const DOW_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']

export function AuditVisualization({ documentId }: Props) {
  const [bucket, setBucket] = useState<'hour' | 'day'>('day')
  const [actorFilter, setActorFilter] = useState<string | null>(null)
  const [actionFilter, setActionFilter] = useState<string | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['audit-viz', documentId, bucket],
    queryFn: () => getAuditViz(documentId, { bucket }),
    staleTime: 30_000,
  })

  // Client-side filter: keep the API response and just hide things
  // the user is filtering out. Cheaper than re-querying.
  const filtered = useMemo(() => {
    if (!data) return null
    const matchEdge = (e: AuditVizSankeyEdge) =>
      (!actorFilter || e.actor === actorFilter) &&
      (!actionFilter || e.action === actionFilter)
    return {
      actors:  actionFilter ? rebuildActors(data.sankey_edges, actionFilter, data.actors) : data.actors,
      actions: actorFilter  ? rebuildActions(data.sankey_edges, actorFilter, data.actions) : data.actions,
      edges:   data.sankey_edges.filter(matchEdge),
      timeBuckets: data.time_buckets,
      heatmap: data.heatmap,
      total: data.total_events,
    }
  }, [data, actorFilter, actionFilter])

  if (isLoading) return <p className="p-3 text-sm text-muted-foreground">Loading audit insights…</p>
  if (error) {
    return (
      <p className="rounded-md border border-red-500/40 bg-red-50/60 p-3 text-sm dark:bg-red-950/20">
        Failed to load audit insights: {(error as Error).message}
      </p>
    )
  }
  if (!filtered || filtered.total === 0) {
    return <p className="p-3 text-sm text-muted-foreground">No audit events yet for this document.</p>
  }

  return (
    <div className="space-y-4">
      <header className="flex flex-wrap items-center gap-2 text-xs">
        <span className="font-semibold">{filtered.total} events</span>
        <span className="text-muted-foreground">·</span>
        <div className="flex items-center gap-1">
          <button
            onClick={() => setBucket('day')}
            className={`rounded-full border px-2 py-0.5 ${bucket === 'day' ? 'border-foreground' : 'border-border text-muted-foreground'}`}
          >
            Day
          </button>
          <button
            onClick={() => setBucket('hour')}
            className={`rounded-full border px-2 py-0.5 ${bucket === 'hour' ? 'border-foreground' : 'border-border text-muted-foreground'}`}
          >
            Hour
          </button>
        </div>
        {(actorFilter || actionFilter) && (
          <button
            onClick={() => { setActorFilter(null); setActionFilter(null) }}
            className="ms-auto inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5 text-muted-foreground hover:text-foreground"
          >
            <XIcon className="h-3 w-3" /> Clear filters
          </button>
        )}
      </header>

      {(actorFilter || actionFilter) && (
        <div className="flex items-center gap-2 rounded-md border border-violet-500/30 bg-violet-50/40 px-2 py-1.5 text-xs dark:bg-violet-950/15">
          <Filter className="h-3.5 w-3.5 text-violet-500" />
          <span>Filtered to</span>
          {actorFilter && <FilterChip onClear={() => setActorFilter(null)}>actor:{actorFilter.slice(0, 8)}…</FilterChip>}
          {actionFilter && <FilterChip onClear={() => setActionFilter(null)}>action:{actionFilter}</FilterChip>}
        </div>
      )}

      <Sparkline buckets={filtered.timeBuckets} />

      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <BarsPanel
          title="Top actors"
          icon={Users}
          // Rows with no resolvable actor rendered as a nameless bar
          // ("a bar with value 1 and no label"). Name them explicitly
          // rather than shipping an anonymous row.
          rows={filtered.actors
            .slice(0, 8)
            .map((a) => ({ id: a.id, label: a.name?.trim() || 'Unknown user', count: a.count }))}
          activeId={actorFilter ?? undefined}
          onClick={(id) => setActorFilter(actorFilter === id ? null : id)}
        />
        <BarsPanel
          title="Top actions"
          icon={Activity}
          rows={filtered.actions.slice(0, 8).map((a) => ({ id: a.name, label: prettyAction(a.name), count: a.count }))}
          activeId={actionFilter ?? undefined}
          onClick={(id) => setActionFilter(actionFilter === id ? null : id)}
        />
      </div>

      <SankeyGrid edges={filtered.edges} />

      <Heatmap cells={filtered.heatmap} />
    </div>
  )
}

// ---- Sparkline ----------------------------------------------------

function Sparkline({ buckets }: { buckets: AuditVizTimeBucket[] }) {
  if (buckets.length === 0) return null
  const max = Math.max(...buckets.map((b) => b.count), 1)
  const width = Math.max(buckets.length * 12, 100)
  const height = 40
  return (
    <section className="space-y-1">
      <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">Activity over time</div>
      <svg viewBox={`0 0 ${width} ${height}`} className="block w-full" preserveAspectRatio="none">
        {buckets.map((b, i) => {
          const h = (b.count / max) * (height - 2)
          return (
            <rect
              key={b.ts}
              x={i * 12 + 1}
              y={height - h}
              width={10}
              height={h}
              className="fill-violet-500/70"
            >
              <title>{`${formatDateTime(b.ts)} · ${b.count} events`}</title>
            </rect>
          )
        })}
      </svg>
    </section>
  )
}

// ---- Bars panel ---------------------------------------------------

function BarsPanel({
  title,
  icon: Icon,
  rows,
  activeId,
  onClick,
}: {
  title: string
  icon: any
  rows: { id: string; label: string; count: number }[]
  activeId?: string
  onClick: (id: string) => void
}) {
  const max = Math.max(...rows.map((r) => r.count), 1)
  return (
    <section className="rounded-md border border-border bg-card p-3">
      <header className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        <Icon className="h-3.5 w-3.5" />
        {title}
      </header>
      <ul className="space-y-1">
        {rows.map((r) => {
          const active = activeId === r.id
          return (
            <li key={r.id}>
              <button
                onClick={() => onClick(r.id)}
                className={`flex w-full items-center gap-2 rounded-md px-2 py-1 text-xs transition-colors ${
                  active
                    ? 'bg-violet-500/20 text-foreground'
                    : 'hover:bg-accent'
                }`}
              >
                <span className="w-32 truncate text-start">{r.label}</span>
                <span className="flex-1">
                  <span className="inline-block h-1.5 overflow-hidden rounded-full bg-muted" style={{ width: '100%' }}>
                    <span
                      className="block h-full bg-violet-500"
                      style={{ width: `${(r.count / max) * 100}%` }}
                    />
                  </span>
                </span>
                <span className="w-8 text-end font-mono">{r.count}</span>
              </button>
            </li>
          )
        })}
      </ul>
    </section>
  )
}

// ---- Sankey (simplified — actor → action grouped grid) ----------

function SankeyGrid({ edges }: { edges: AuditVizSankeyEdge[] }) {
  if (edges.length === 0) return null
  // Build a unique sorted list of actors and actions in their order
  // of appearance (already count-desc from the API).
  const actors = uniq(edges.map((e) => e.actor))
  const actions = uniq(edges.map((e) => e.action))
  const max = Math.max(...edges.map((e) => e.count), 1)
  const cellSize = (count: number) => Math.max(4, (count / max) * 20)
  return (
    <section className="rounded-md border border-border bg-card p-3">
      <header className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        <BarChart3 className="h-3.5 w-3.5" />
        Actor × action
      </header>
      <div className="overflow-x-auto">
        <table className="text-[11px]">
          <thead>
            <tr>
              <th />
              {actions.map((a) => (
                <th key={a} className="px-1.5 py-1 align-bottom">
                  <div className="rotate-[-30deg] whitespace-nowrap text-muted-foreground">{prettyAction(a)}</div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {actors.map((act) => (
              <tr key={act}>
                <td className="pe-2 text-end font-mono text-muted-foreground">{act.slice(0, 8)}…</td>
                {actions.map((action) => {
                  const e = edges.find((x) => x.actor === act && x.action === action)
                  if (!e) return <td key={action} />
                  const size = cellSize(e.count)
                  return (
                    <td key={action} className="px-1 py-1">
                      <div
                        className="rounded-sm bg-violet-500/70"
                        style={{ width: size, height: size }}
                        title={`${prettyAction(action)} · ${e.count}`}
                      />
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// ---- Heatmap (24×7) -----------------------------------------------

function Heatmap({ cells }: { cells: AuditVizHeatmapCell[] }) {
  if (cells.length === 0) return null
  const grid: number[][] = Array.from({ length: 7 }, () => Array(24).fill(0))
  for (const c of cells) grid[c.dow][c.hour] = c.count
  const max = Math.max(...cells.map((c) => c.count), 1)
  return (
    <section className="rounded-md border border-border bg-card p-3">
      <header className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        Hour of day (UTC)
      </header>
      <div className="overflow-x-auto">
        <table className="text-[10px]">
          <thead>
            <tr>
              <th />
              {Array.from({ length: 24 }, (_, h) => (
                <th key={h} className="w-5 text-center text-muted-foreground">{h % 3 === 0 ? h : ''}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {grid.map((row, dow) => (
              <tr key={dow}>
                <td className="pe-2 text-end text-muted-foreground">{DOW_LABELS[dow]}</td>
                {row.map((count, hr) => (
                  <td key={hr} className="p-px">
                    <div
                      className="h-4 w-4 rounded-sm"
                      style={{
                        backgroundColor: count === 0
                          ? 'var(--color-muted, rgba(0,0,0,0.05))'
                          : `rgba(139, 92, 246, ${0.2 + 0.8 * (count / max)})`,
                      }}
                      title={`${DOW_LABELS[dow]} ${hr}:00 UTC · ${count} events`}
                    />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

// ---- helpers ------------------------------------------------------

function uniq<T>(arr: T[]): T[] {
  return Array.from(new Set(arr))
}

function rebuildActors(
  edges: AuditVizSankeyEdge[],
  actionFilter: string,
  fallback: AuditVizActor[],
): AuditVizActor[] {
  const filtered = edges.filter((e) => e.action === actionFilter)
  const map = new Map<string, number>()
  for (const e of filtered) map.set(e.actor, (map.get(e.actor) ?? 0) + e.count)
  return Array.from(map.entries())
    .map(([id, count]) => ({
      id,
      name: fallback.find((a) => a.id === id)?.name ?? id.slice(0, 8),
      count,
    }))
    .sort((a, b) => b.count - a.count)
}

function rebuildActions(
  edges: AuditVizSankeyEdge[],
  actorFilter: string,
  _fallback: AuditVizAction[],
): AuditVizAction[] {
  const filtered = edges.filter((e) => e.actor === actorFilter)
  const map = new Map<string, number>()
  for (const e of filtered) map.set(e.action, (map.get(e.action) ?? 0) + e.count)
  return Array.from(map.entries())
    .map(([name, count]) => ({ name, count }))
    .sort((a, b) => b.count - a.count)
}

function prettyAction(name: string): string {
  // "document.updated" → "Document updated"
  return name.replace(/[_.]/g, ' ').replace(/^\w/, (c) => c.toUpperCase())
}

function FilterChip({ children, onClear }: { children: React.ReactNode; onClear: () => void }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full bg-violet-500/15 px-1.5 py-0.5 font-mono text-violet-800 dark:text-violet-200">
      {children}
      <button onClick={onClear} aria-label="Remove filter"><XIcon className="h-3 w-3" /></button>
    </span>
  )
}
